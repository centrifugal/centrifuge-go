//go:build js
// +build js

package centrifuge

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall/js"
	"time"

	"github.com/centrifugal/protocol"
)

func handleJSError(err *error, onErr func()) {
	r := recover()

	if jsErr, ok := r.(js.Error); ok {
		*err = jsErr

		if onErr != nil {
			onErr()
		}
		return
	}

	if r != nil {
		panic(r)
	}
}

func extractDisconnectWebsocket(err error) *disconnect {
	return nil
}

type websocketTransport struct {
	mu             sync.Mutex
	ws             js.Value
	protocolType   protocol.Type
	commandEncoder protocol.CommandEncoder
	replyCh        chan *protocol.Reply
	config         websocketConfig
	disconnect     *disconnect
	closed         bool
	closeCh        chan struct{}
}

// websocketConfig configures Websocket transport.
type websocketConfig struct {
	// NetDialContext specifies the dial function for creating TCP connections. If
	// NetDialContext is nil, net.DialContext is used.
	NetDialContext func(ctx context.Context, network, addr string) (net.Conn, error)

	// TLSConfig specifies the TLS configuration to use with tls.Client.
	// If nil, the default configuration is used.
	TLSConfig *tls.Config

	// HandshakeTimeout specifies the duration for the handshake to complete.
	HandshakeTimeout time.Duration

	// EnableCompression specifies if the client should attempt to negotiate
	// per message compression (RFC 7692). Setting this value to true does not
	// guarantee that compression will be supported. Currently only "no context
	// takeover" modes are supported.
	EnableCompression bool

	// CookieJar specifies the cookie jar.
	// If CookieJar is nil, cookies are not sent in requests and ignored
	// in responses.
	CookieJar http.CookieJar

	// Header specifies custom HTTP Header to send.
	Header http.Header
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
		ws:             ws,
		replyCh:        make(chan *protocol.Reply),
		config:         config,
		closeCh:        make(chan struct{}),
		commandEncoder: newCommandEncoder(protocolType),
		protocolType:   protocolType,
	}
	go t.reader()
	return t, nil
}

func (t *websocketTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	close(t.closeCh)
	// TODO: close connection.
	return nil
}

func (t *websocketTransport) reader() {
	defer func() { _ = t.Close() }()
	defer close(t.replyCh)

	for {
		time.Sleep(time.Second)
		fmt.Println("implement reading from connection")
		//	_, data, err := t.conn.ReadMessage()
		//	if err != nil {
		//		disconnect := extractDisconnectWebsocket(err)
		//		t.disconnect = disconnect
		//		return
		//	}
		//	//println("<----", strings.Trim(string(data), "\n"))
		//loop:
		//	for {
		//		decoder := newReplyDecoder(t.protocolType, data)
		//		for {
		//			reply, err := decoder.Decode()
		//			if err != nil {
		//				if err == io.EOF {
		//					break loop
		//				}
		//				t.disconnect = &disconnect{Code: disconnectBadProtocol, Reason: "decode error", Reconnect: false}
		//				return
		//			}
		//			select {
		//			case <-t.closeCh:
		//				return
		//			case t.replyCh <- reply:
		//				// Send is blocking here, but slow client will be disconnected
		//				// eventually with `no ping` reason – so we will exit from this
		//				// goroutine.
		//			}
		//		}
		//	}
	}
	fmt.Println("exit from reader")
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
	fmt.Println("writing to connection", string(data))
	var err error
	if t.protocolType == protocol.TypeJSON {
		err = t.writeText(string(data))
	} else {
		err = t.writeBytes(data)
	}
	return err
}

// SendText sends the given string as a text message
// on the WebSocket.
func (t *websocketTransport) writeText(v string) (err error) {
	defer handleJSError(&err, nil)
	t.ws.Call("send", v)
	return err
}

// SendBytes sends the given message as a binary message
// on the WebSocket.
func (t *websocketTransport) writeBytes(v []byte) (err error) {
	defer handleJSError(&err, nil)
	t.ws.Call("send", uint8Array(v))
	return err
}

func uint8Array(src []byte) js.Value {
	uint8Array := js.Global().Get("Uint8Array").New(len(src))
	js.CopyBytesToJS(uint8Array, src)
	return uint8Array
}

func (t *websocketTransport) Read() (*protocol.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}
	return reply, nil, nil
}
