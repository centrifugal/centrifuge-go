package centrifuge

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge-mobile/internal/proto"
	"github.com/gorilla/websocket"
)

func extractDisconnectWebsocket(err error) *disconnect {
	if err != nil {
		if closeErr, ok := err.(*websocket.CloseError); ok {
			var disconnect disconnect
			err := json.Unmarshal([]byte(closeErr.Text), &disconnect)
			if err == nil {
				return &disconnect
			}
		}
	}
	return nil
}

type websocketTransport struct {
	mu             sync.Mutex
	conn           *websocket.Conn
	encoding       proto.Encoding
	commandEncoder proto.CommandEncoder
	replyDecoder   proto.ReplyDecoder
	replyCh        chan *proto.Reply
	config         WebsocketConfig
	disconnect     *disconnect
	closed         bool
	closeCh        chan struct{}
}

// WebsocketConfig configures Websocket transport.
type WebsocketConfig struct {
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
}

func newWebsocketTransport(url string, encoding proto.Encoding, config WebsocketConfig) (transport, error) {
	wsHeaders := http.Header{}
	dialer := websocket.DefaultDialer

	dialer.HandshakeTimeout = config.HandshakeTimeout
	dialer.EnableCompression = config.EnableCompression
	dialer.TLSClientConfig = config.TLSConfig
	dialer.Jar = config.CookieJar

	conn, resp, err := dialer.Dial(url, wsHeaders)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("Wrong status code while connecting to server: '%d'", resp.StatusCode)
	}

	t := &websocketTransport{
		conn:           conn,
		replyCh:        make(chan *proto.Reply, 128),
		config:         config,
		closeCh:        make(chan struct{}),
		commandEncoder: proto.NewCommandEncoder(encoding),
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
	return t.conn.Close()
}

func (t *websocketTransport) reader() {
	defer t.Close()
	defer close(t.replyCh)

	for {
		_, data, err := t.conn.ReadMessage()
		if err != nil {
			disconnect := extractDisconnectWebsocket(err)
			t.disconnect = disconnect
			return
		}
	loop:
		for {
			decoder := proto.NewReplyDecoder(t.encoding, data)
			for {
				reply, err := decoder.Decode()
				if err != nil {
					if err == io.EOF {
						break loop
					}
					return
				}
				select {
				case <-t.closeCh:
					return
				case t.replyCh <- reply:
				default:
					// Can't keep up with server message rate.
					t.disconnect = &disconnect{Reason: "client slow", Reconnect: true}
					return
				}
			}
		}
	}
}

func (t *websocketTransport) Write(cmd *proto.Command, timeout time.Duration) error {
	data, err := t.commandEncoder.Encode(cmd)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if timeout > 0 {
		t.conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	if t.encoding == proto.EncodingJSON {
		err = t.conn.WriteMessage(websocket.TextMessage, data)
	} else {
		err = t.conn.WriteMessage(websocket.BinaryMessage, data)
	}
	if timeout > 0 {
		t.conn.SetWriteDeadline(time.Time{})
	}
	return err
}

func (t *websocketTransport) Read() (*proto.Reply, *disconnect, error) {
	reply, ok := <-t.replyCh
	if !ok {
		return nil, t.disconnect, io.EOF
	}
	return reply, nil, nil
}
