package centrifuge

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/centrifugal/protocol"

	"github.com/gorilla/websocket"
)

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
	// guarantee that compression will be supported. Currently, only "no context
	// takeover" modes are supported.
	EnableCompression bool

	// CookieJar specifies the cookie jar.
	// If CookieJar is nil, cookies are not sent in requests and ignored
	// in responses.
	CookieJar http.CookieJar

	// Header specifies custom HTTP Header to send.
	Header http.Header
}

func constructDisconnect(code uint32, reason string) *disconnect {
	reconnect := code < 3500 || code >= 5000 || (code >= 4000 && code < 4500)
	if code < 3000 {
		switch code {
		case websocket.CloseMessageTooBig:
			code = disconnectMessageSizeLimit
			reason = "message size limit"
		default:
			// We expose codes defined by Centrifuge protocol, hiding
			// details about transport-specific error codes. We may have extra
			// optional transportCode field in the future.
			code = connectingTransportClosed
		}
	}
	return &disconnect{
		Code:      code,
		Reason:    reason,
		Reconnect: reconnect,
	}
}

type websocketTransportCommon struct {
	mu             sync.Mutex
	protocolType   protocol.Type
	commandEncoder protocol.CommandEncoder
	replyCh        chan *protocol.Reply
	config         websocketConfig
	disconnect     *disconnect
	closed         bool
	closeCh        chan struct{}
}
