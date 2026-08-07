package centrifuge

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
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
				reconnect := code < 3500 || code >= 5000 || (code >= 4000 && code < 4500)
				if code < 3000 {
					switch code {
					case websocket.CloseMessageTooBig:
						code = disconnectMessageSizeLimit
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
		}
	}
	return nil
}

type websocketTransport struct {
	mu             sync.Mutex
	conn           *websocket.Conn
	protocolType   protocol.Type
	commandEncoder protocol.CommandEncoder
	replyCh        chan *protocol.Reply
	config         websocketConfig
	disconnect     *disconnect
	closed         bool
	closeCh        chan struct{}

	// codec decodes frames once the server activated dictionary compression.
	// pendingCodec holds a dictionary seen inside the frame currently being
	// decoded; it is promoted only after that frame is fully consumed, because
	// the frame carrying a dictionary is itself sent uncompressed. Both are
	// touched only by the reader goroutine.
	codec        *protocol.DeflateFrameCodec
	pendingCodec *protocol.DeflateFrameCodec
	// cache remembers the structure dictionary across this client's connections,
	// so a reconnect costs an id rather than a transfer. It is shared with the
	// Client and touched only by the reader goroutine plus connect setup.
	cache *dictionaryCache
	// fromCache records that the codec in use came from the cache, so a decode
	// failure can be attributed to a stale entry and retried rather than treated
	// as an unrecoverable protocol error.
	fromCache bool
	// unusableDict is set when the server named a dictionary this client does not
	// have. Nothing after that frame can be decoded, so the connection has to be
	// restarted rather than left to misread everything.
	unusableDict bool

	// Compression accounting. Written by the reader goroutine, read by callers
	// of Client.CompressionStats, hence atomics.
	compressedIn   atomic.Int64
	uncompressedIn atomic.Int64
	dictionaryIn   atomic.Int64
	framesIn       atomic.Int64
}

// compressionStats returns what this connection measured about compression.
func (t *websocketTransport) compressionStats() CompressionStats {
	id := ""
	if t.codec != nil {
		id = t.codec.ID()
	}
	return CompressionStats{
		Active:            t.codec != nil,
		DictionaryID:      id,
		Frames:            t.framesIn.Load(),
		BytesReceived:     t.compressedIn.Load(),
		BytesDecompressed: t.uncompressedIn.Load(),
		DictionaryBytes:   t.dictionaryIn.Load(),
	}
}

// maxDecompressedFrameSize bounds a frame after decompression so that a small
// crafted frame can not be inflated into an unbounded allocation.
const maxDecompressedFrameSize = 16 * 1024 * 1024

// maxDictionarySize bounds a dictionary after inflation, for the same reason.
// Useful dictionaries are a few kilobytes; this leaves generous headroom.
const maxDictionarySize = 1 * 1024 * 1024

// dictionaryFrom builds a codec from a Dictionary the server sent, returning
// nil when there is nothing usable in it.
//
// cacheable says whether the dictionary is worth keeping past this connection.
// The one that arrives in the connect reply is: it belongs to this client's
// profile and changes only when the server publishes a new version, so keeping
// it turns the next connect into an id and no transfer. One that arrives later
// in a push is connection-scoped and is not kept.
//
// A dictionary with an id but no content means "use the one you already have" -
// the server recognised the id this client advertised at connect, so there was
// nothing to send. An id is a hash of the content, so a match is byte identical
// by construction.
func (t *websocketTransport) dictionaryFrom(d *protocol.Dictionary, cacheable bool) *protocol.DeflateFrameCodec {
	if d == nil {
		return nil
	}
	if len(d.Data) == 0 && d.DataB64 == "" {
		if t.cache != nil {
			if dict, ok := t.cache.get(d.Id); ok {
				t.fromCache = true
				return protocol.NewDeflateFrameCodec(d.Id, dict)
			}
		}
		// The server named something this client does not have - it was evicted
		// between advertising it and now, or the server is confused. Either way
		// nothing after this frame can be decoded.
		t.unusableDict = true
		return nil
	}
	// Protobuf connections carry the dictionary as raw bytes; JSON connections
	// have to base64 it, because a bytes field carries raw JSON in that protocol.
	// Exactly one is set, so prefer the cheap one and fall back.
	var raw []byte
	if len(d.Data) > 0 {
		raw = d.Data
	} else {
		decoded, err := base64.StdEncoding.DecodeString(d.DataB64)
		if err != nil || len(decoded) == 0 {
			return nil
		}
		raw = decoded
	}
	// Content is always deflated: the frame carrying it cannot be compressed,
	// because nothing is installed yet to compress it against.
	inflated, err := protocol.InflateDictionary(raw, maxDictionarySize)
	if err != nil {
		return nil
	}
	raw = inflated
	if cacheable && t.cache != nil && d.Id != "" {
		t.cache.put(d.Id, raw)
	}
	t.fromCache = false
	return protocol.NewDeflateFrameCodec(d.Id, raw)
}

// websocketConfig configures Websocket transport.
type websocketConfig struct {
	// Proxy specifies a function to return a proxy for a given Request.
	// If the function returns a non-nil error, the request is aborted with the
	// provided error. If function returns a nil *URL, no proxy is used.
	// If Proxy is nil then http.ProxyFromEnvironment will be used.
	Proxy func(*http.Request) (*url.URL, error)

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

func newWebsocketTransport(url string, protocolType protocol.Type, config websocketConfig, cache *dictionaryCache) (transport, error) {
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
		conn:           conn,
		replyCh:        make(chan *protocol.Reply),
		config:         config,
		closeCh:        make(chan struct{}),
		commandEncoder: newCommandEncoder(protocolType),
		protocolType:   protocolType,
		cache:          cache,
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
			disconnect := extractDisconnectWebsocket(err)
			t.disconnect = disconnect
			return
		}
		if t.codec != nil {
			compressedLen := len(data)
			data, err = t.codec.Decompress(nil, data, maxDecompressedFrameSize)
			if err != nil {
				// A cached dictionary that cannot decode is a stale or corrupted
				// entry: drop it and retry, or the client would advertise it again
				// on every reconnect and never recover. Anything else is a real
				// protocol error and is not worth retrying.
				retry := false
				if t.fromCache && t.cache != nil {
					t.cache.forget(t.codec.ID())
					retry = true
				}
				t.disconnect = &disconnect{Code: disconnectBadProtocol, Reason: "frame decode error", Reconnect: retry}
				return
			}
			t.framesIn.Add(1)
			t.compressedIn.Add(int64(compressedLen))
			t.uncompressedIn.Add(int64(len(data)))
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
				// Compression is set up by the connect reply, which is the frame
				// that carries the dictionary. A push may replace it later, which
				// the protocol allows but the server does not currently do.
				offered, cacheable := offeredDictionary(reply)
				if c := t.dictionaryFrom(offered, cacheable); c != nil {
					// Applied after this frame completes - see pendingCodec.
					t.pendingCodec = c
					// A dictionary that had to be sent is a real cost paid on this
					// connection, so it is recorded and later subtracted from bytes
					// saved. One reused from the cache crossed no wire and is not.
					if !t.fromCache {
						t.dictionaryIn.Add(int64(len(c.Dict())))
					}
				} else if t.unusableDict {
					// The server named a dictionary this client does not hold, so
					// nothing after this frame can be decoded. Forget the id and
					// start over rather than misread everything that follows.
					t.unusableDict = false
					if t.cache != nil && offered != nil {
						t.cache.forget(offered.Id)
					}
					t.disconnect = &disconnect{Code: disconnectBadProtocol,
						Reason: "unknown dictionary", Reconnect: true}
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
		if t.pendingCodec != nil {
			t.codec = t.pendingCodec
			t.pendingCodec = nil
		}
	}
}

// offeredDictionary returns the dictionary a reply carries, if any, and whether
// it is worth caching past this connection. Unknown ConnectionState fields are
// ignored on purpose: the message is meant to carry further connection-level
// state in future without breaking older clients.
func offeredDictionary(reply *protocol.Reply) (*protocol.Dictionary, bool) {
	if reply.Connect != nil && reply.Connect.Dict != nil {
		return reply.Connect.Dict, true
	}
	if reply.Push != nil && reply.Push.State != nil && reply.Push.State.Dict != nil {
		return reply.Push.State.Dict, false
	}
	return nil, false
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
