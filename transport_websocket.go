//go:build !js

package centrifuge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/centrifugal/protocol"
	"github.com/gorilla/websocket"
)

func extractDisconnectWebsocket(err error) *disconnect {
	if err != nil {
		if closeErr, ok := err.(*websocket.CloseError); ok {
			var d disconnect
			err := json.Unmarshal([]byte(closeErr.Text), &d)
			if err == nil {
				return &d
			} else {
				code := uint32(closeErr.Code)
				reason := closeErr.Text
				return constructDisconnect(code, reason)
			}
		}
	}
	return nil
}

type websocketTransport struct {
	conn *websocket.Conn
	websocketTransportCommon
}

func newWebsocketTransport(url string, protocolType protocol.Type, config websocketConfig) (transport, error) {
	wsHeaders := config.Header

	dialer := &websocket.Dialer{}
	if config.Proxy != nil {
		dialer.Proxy = config.Proxy
	} else {
		dialer.Proxy = http.ProxyFromEnvironment
	}
	dialer.NetDialContext = config.NetDialContext

	dialer.HandshakeTimeout = config.HandshakeTimeout
	dialer.EnableCompression = config.EnableCompression
	dialer.TLSClientConfig = config.TLSConfig
	dialer.Jar = config.CookieJar

	if protocolType == protocol.TypeProtobuf {
		dialer.Subprotocols = []string{"centrifuge-protobuf"}
	}

	conn, resp, err := dialer.Dial(url, wsHeaders)
	if err != nil {
		return nil, fmt.Errorf("error dial: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("wrong status code while connecting to server: %d", resp.StatusCode)
	}

	t := &websocketTransport{
		conn: conn,
		websocketTransportCommon: websocketTransportCommon{
			replyCh:        make(chan *protocol.Reply),
			config:         config,
			closeCh:        make(chan struct{}),
			commandEncoder: newCommandEncoder(protocolType),
			protocolType:   protocolType,
		},
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
	_ = t.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	return t.conn.Close()
}

func (t *websocketTransport) reader() {
	defer func() { _ = t.Close() }()
	defer close(t.replyCh)

	for {
		_, data, err := t.conn.ReadMessage()
		if err != nil {
			t.disconnect = extractDisconnectWebsocket(err)
			return
		}
		//println("<----", strings.Trim(string(data), "\n"))
	loop:
		for {
			decoder := newReplyDecoder(t.protocolType, data)
			for {
				reply, err := decoder.Decode()
				if err != nil {
					if err == io.EOF {
						break loop
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
	if timeout > 0 {
		_ = t.conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	//println("---->", strings.Trim(string(data), "\n"))
	var err error
	if t.protocolType == protocol.TypeJSON {
		err = t.conn.WriteMessage(websocket.TextMessage, data)
	} else {
		err = t.conn.WriteMessage(websocket.BinaryMessage, data)
	}
	if timeout > 0 {
		_ = t.conn.SetWriteDeadline(time.Time{})
	}
	return err
}

func (t *websocketTransport) Read() (*protocol.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}
	return reply, nil, nil
}
