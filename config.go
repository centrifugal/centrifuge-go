package centrifuge

import (
	"crypto/tls"
	"net/http"
	"time"
)

const (
	// DefaultHandshakeTimeout ...
	DefaultHandshakeTimeout = time.Second
	// DefaultReadTimeout ...
	DefaultReadTimeout = 5 * time.Second
	// DefaultWriteTimeout ...
	DefaultWriteTimeout = time.Second
	// DefaultPingInterval ...
	DefaultPingInterval = 25 * time.Second
	// DefaultPrivateChannelPrefix ...
	DefaultPrivateChannelPrefix = "$"
)

// Config contains various client options.
type Config struct {
	// PrivateChannelPrefix is private channel prefix.
	PrivateChannelPrefix string
	// ReadTimeout is how long to wait read operations to complete.
	ReadTimeout time.Duration
	// WriteTimeout is Websocket write timeout.
	WriteTimeout time.Duration
	// PingInterval is how often to send ping commands to server.
	PingInterval time.Duration
	// HandshakeTimeout specifies the duration for the handshake to complete.
	HandshakeTimeout time.Duration
	// TLSConfig specifies the TLS configuration to use with tls.Client.
	// If nil, the default configuration is used.
	TLSConfig *tls.Config
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

// DefaultConfig returns Config with default options.
func DefaultConfig() Config {
	return Config{
		PingInterval:         DefaultPingInterval,
		ReadTimeout:          DefaultReadTimeout,
		WriteTimeout:         DefaultWriteTimeout,
		HandshakeTimeout:     DefaultHandshakeTimeout,
		PrivateChannelPrefix: DefaultPrivateChannelPrefix,
		Header:               http.Header{},
	}
}
