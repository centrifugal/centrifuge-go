//go:build js

package centrifuge

import (
	"io"
	"reflect"
	"sync"
	"syscall/js"
	"time"

	"github.com/centrifugal/protocol"
)

type websocketTransport struct {
	websocketTransportCommon

	ws               js.Value
	releaseOnClose   func()
	releaseOnMessage func()
	readSignal       chan struct{}
	readBufMu        sync.Mutex
	readBuf          []jsMessageEvent
}

func newWebsocketTransport(url string, protocolType protocol.Type, config websocketConfig) (transport, error) {
	protocols := make([]interface{}, 0)
	if protocolType == protocol.TypeProtobuf {
		protocols = []interface{}{"centrifuge-protobuf"}
	}

	ws := js.Global().Get("WebSocket").New(url, protocols)
	if protocolType == protocol.TypeProtobuf {
		ws.Set("binaryType", "arraybuffer")
	}

	t := &websocketTransport{
		websocketTransportCommon: websocketTransportCommon{
			replyCh:        make(chan *protocol.Reply),
			config:         config,
			closeCh:        make(chan struct{}),
			commandEncoder: newCommandEncoder(protocolType),
			protocolType:   protocolType,
		},
		ws:         ws,
		readSignal: make(chan struct{}, 1),
	}

	t.releaseOnClose = t.onClose(func(e jsCloseEvent) {
		t.disconnect = extractDisconnectWebsocket(e)
		_ = t.Close()

		t.releaseOnClose()
		t.releaseOnMessage()
	})

	t.releaseOnMessage = t.onMessage(func(e jsMessageEvent) {
		t.readBufMu.Lock()
		defer t.readBufMu.Unlock()
		t.readBuf = append(t.readBuf, e)
		// Let the read goroutine know there is something in readBuf.
		select {
		case t.readSignal <- struct{}{}:
		default:
		}
	})

	openCh := make(chan struct{})
	releaseOpen := t.onOpen(func(e js.Value) {
		close(openCh)
	})
	defer releaseOpen()

	select {
	case <-time.After(config.HandshakeTimeout):
		t.Close()
		return nil, ErrTimeout
	case <-openCh:
		go t.reader()
		return t, nil
	case <-t.closeCh:
		return nil, ErrClientClosed
	}
}

func (t *websocketTransport) addEventListener(eventType string, fn func(e js.Value)) func() {
	f := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		fn(args[0])
		return nil
	})
	t.ws.Call("addEventListener", eventType, f)
	return func() {
		t.ws.Call("removeEventListener", eventType, f)
		f.Release()
	}
}

func (t *websocketTransport) onOpen(fn func(e js.Value)) (remove func()) {
	return t.addEventListener("open", fn)
}

type jsMessageEvent struct {
	// string or []byte.
	Data interface{}
}

func (t *websocketTransport) onMessage(fn func(m jsMessageEvent)) (remove func()) {
	return t.addEventListener("message", func(e js.Value) {
		var data interface{}
		arrayBuffer := e.Get("data")
		if arrayBuffer.Type() == js.TypeString {
			data = arrayBuffer.String()
		} else {
			data = bytesFromArrayBuffer(arrayBuffer)
		}
		fn(jsMessageEvent{
			Data: data,
		})
		return
	})
}

type jsCloseEvent struct {
	Code   uint16
	Reason string
}

func (t *websocketTransport) onClose(fn func(jsCloseEvent)) (remove func()) {
	return t.addEventListener("close", func(e js.Value) {
		ce := jsCloseEvent{
			Code:   uint16(e.Get("code").Int()),
			Reason: e.Get("reason").String(),
		}
		fn(ce)
	})
}

func (t *websocketTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.closeCh)
	return t.close()
}

func (t *websocketTransport) close() (err error) {
	defer handleJSError(&err)
	t.ws.Call("close", 1000, "normal closure")
	return err
}

func (t *websocketTransport) reader() {
	defer func() { _ = t.Close() }()
	defer close(t.replyCh)

LOOP:
	for {
		select {
		case <-t.closeCh:
			break LOOP
		default:
		}
		data, err := t.read()
		if err != nil {
			return
		}
		//println("<----", strings.Trim(string(data), "\n"))

		decoder := newReplyDecoder(t.protocolType, data)

	DECODE:
		for {
			reply, err := decoder.Decode()
			if err != nil {
				if err == io.EOF {
					break DECODE
				}
				t.disconnect = &disconnect{Code: disconnectBadProtocol, Reason: "decode error", Reconnect: false}
				return
			}
			select {
			case <-t.closeCh:
				return
			case t.replyCh <- reply:
				// Send is blocking here, but slow client will be disconnected
				// eventually with `no ping` reason – so we will exit from this
				// goroutine.
			}
		}
	}
}

func (t *websocketTransport) read() ([]byte, error) {
	select {
	case <-t.closeCh:
		return nil, io.EOF
	case <-t.readSignal:
	}

	t.readBufMu.Lock()
	defer t.readBufMu.Unlock()

	ev := t.readBuf[0]
	// We copy the messages forward and decrease the size
	// of the slice to avoid reallocating.
	copy(t.readBuf, t.readBuf[1:])
	t.readBuf = t.readBuf[:len(t.readBuf)-1]

	if len(t.readBuf) > 0 {
		// Next time we read, we'll grab the message.
		select {
		case t.readSignal <- struct{}{}:
		default:
		}
	}

	switch p := ev.Data.(type) {
	case string:
		return []byte(p), nil
	case []byte:
		return p, nil
	default:
		tp := reflect.TypeOf(ev.Data).String()
		panic("websocket: unexpected data type on read: " + tp)
	}
}

func (t *websocketTransport) Write(cmd *protocol.Command, timeout time.Duration) error {
	data, err := t.commandEncoder.Encode(cmd)
	if err != nil {
		return err
	}
	return t.writeData(data, timeout)
}

func (t *websocketTransport) writeData(data []byte, timeout time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var err error
	if t.protocolType == protocol.TypeJSON {
		err = t.writeText(string(data))
	} else {
		err = t.writeBytes(data)
	}
	return err
}

func (t *websocketTransport) writeText(v string) (err error) {
	defer handleJSError(&err)
	t.ws.Call("send", v)
	return err
}

func (t *websocketTransport) writeBytes(v []byte) (err error) {
	defer handleJSError(&err)
	t.ws.Call("send", bytesToUint8Array(v))
	return err
}

func (t *websocketTransport) Read() (*protocol.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}
	return reply, nil, nil
}

func bytesToUint8Array(src []byte) js.Value {
	uint8Array := js.Global().Get("Uint8Array").New(len(src))
	js.CopyBytesToJS(uint8Array, src)
	return uint8Array
}

func bytesFromArrayBuffer(arrayBuffer js.Value) []byte {
	uint8Array := js.Global().Get("Uint8Array").New(arrayBuffer)
	dst := make([]byte, uint8Array.Length())
	js.CopyBytesToGo(dst, uint8Array)
	return dst
}

func handleJSError(err *error) {
	r := recover()

	if jsErr, ok := r.(js.Error); ok {
		*err = jsErr
		return
	}

	if r != nil {
		panic(r)
	}
}

func extractDisconnectWebsocket(e jsCloseEvent) *disconnect {
	return constructDisconnect(uint32(e.Code), e.Reason)
}
