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
						reconnect = false
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
	// the frame carrying a dictionary is itself sent uncompressed.
	//
	// Both are written by the reader goroutine and read by callers of
	// Client.DictionaryCompressionStats, so both are atomic - the counters
	// beneath were already, and these were the same shared state wearing a
	// plain pointer.
	codec        atomic.Pointer[protocol.DeflateFrameCodec]
	pendingCodec atomic.Pointer[protocol.DeflateFrameCodec]
	// accepted records that the server confirmed dictionary compression in the
	// connect reply. It answers a different question from codec being set: this
	// one is known as soon as the reply is read, which is when a caller in an
	// OnConnected handler asks.
	accepted atomic.Bool
	// cache remembers the structure dictionary across this client's connections,
	// so a reconnect costs an id rather than a transfer. It is shared with the
	// Client and touched only by the reader goroutine plus connect setup.
	cache *dictionaryCache
	// fromCache records that the codec in use came from the cache, so its
	// transfer is not counted as paid on this connection.
	fromCache bool

	// Compression accounting. Written by the reader goroutine, read by callers
	// of Client.DictionaryCompressionStats, hence atomics.
	compressedIn   atomic.Int64
	uncompressedIn atomic.Int64
	dictionaryIn   atomic.Int64
	// dictWireBytes is the encoded size of the dictionary as it arrived, set
	// while decoding it and read once by the frame that carried it.
	dictWireBytes int64
	framesIn      atomic.Int64
}

// dictionaryCompressionStats returns what this connection measured.
func (t *websocketTransport) dictionaryCompressionStats() DictionaryCompressionStats {
	codec := t.codec.Load()
	if codec == nil {
		codec = t.pendingCodec.Load()
	}
	id := ""
	if codec != nil {
		id = codec.ID()
	}
	return DictionaryCompressionStats{
		Accepted:          t.accepted.Load(),
		Active:            codec != nil,
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
// nil when none was sent, and an error saying why when one was sent that this
// client can not use.
//
// What it builds is always worth keeping past this connection: a dictionary
// belongs to this client's profile and changes only when the server publishes a
// new version, so caching it turns the next connect into an id and no transfer.
//
// A dictionary with an id but no content means "use the one you already have" -
// the server recognised the id this client advertised at connect, so there was
// nothing to send. An id is a hash of the content, so a match is byte identical
// by construction.
func (t *websocketTransport) dictionaryFrom(d *protocol.Dictionary) (*protocol.DeflateFrameCodec, error) {
	if d == nil {
		return nil, nil
	}
	if len(d.Data) == 0 && d.DataB64 == "" {
		if t.cache != nil {
			if dict, ok := t.cache.get(d.Id); ok {
				t.fromCache = true
				return newDecodeCodec(d.Id, dict), nil
			}
		}
		// The server named something this client does not have - it was evicted
		// between advertising it and now, or the server is confused. Either way
		// nothing after this frame can be decoded.
		return nil, fmt.Errorf("server named dictionary %s, which this client does not hold", d.Id)
	}
	// Protobuf connections carry the dictionary as raw bytes; JSON connections
	// have to base64 it, because a bytes field carries raw JSON in that protocol.
	// Exactly one is set, so prefer the cheap one and fall back.
	//
	// dictWireBytes is measured here, on the encoded form, because that is what
	// actually crossed the wire: the content is deflated, and on JSON it is
	// base64 on top of that. Charging the inflated size instead - which is what
	// the codec holds - overstated the cost of the structure dictionary by most
	// of its size, and did so in the operator's dashboards as well as the SDK's.
	var raw []byte
	if len(d.Data) > 0 {
		raw = d.Data
		t.dictWireBytes = int64(len(d.Data))
	} else {
		decoded, err := base64.StdEncoding.DecodeString(d.DataB64)
		if err != nil {
			return nil, fmt.Errorf("dictionary %s is not valid base64: %w", d.Id, err)
		}
		if len(decoded) == 0 {
			return nil, fmt.Errorf("dictionary %s is empty", d.Id)
		}
		raw = decoded
		t.dictWireBytes = int64(len(d.DataB64))
	}
	// Content is always deflated: the frame carrying it cannot be compressed,
	// because nothing is installed yet to compress it against.
	inflated, err := protocol.InflateDictionary(raw, maxDictionarySize)
	if err != nil {
		return nil, fmt.Errorf("dictionary %s does not inflate: %w", d.Id, err)
	}
	raw = inflated
	if t.cache != nil && d.Id != "" {
		t.cache.put(d.Id, raw)
	}
	t.fromCache = false
	return newDecodeCodec(d.Id, raw), nil
}

// newDecodeCodec builds a codec that only ever decompresses. The level applies
// to compressing alone, so any valid one does.
func newDecodeCodec(id string, dict []byte) *protocol.DeflateFrameCodec {
	return protocol.NewDeflateFrameCodec(id, dict, protocol.MinDictionaryCompressionLevel)
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
		if codec := t.codec.Load(); codec != nil {
			compressedLen := len(data)
			data, err = codec.Decompress(nil, data, maxDecompressedFrameSize)
			if err != nil {
				// A frame that does not decode is a protocol error, terminal like
				// any other. The dictionary is forgotten, so a later Connect does
				// not offer it again.
				if t.cache != nil {
					t.cache.forget(codec.ID())
				}
				t.disconnect = &disconnect{Code: disconnectBadProtocol, Reconnect: false,
					Reason: fmt.Sprintf("dictionary compression: frame does not decode with dictionary %s: %v", codec.ID(), err)}
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
				// that carries the dictionary. There is no way to replace it
				// mid-connection: the frame announcing a new dictionary would have
				// to be encoded against the old one, so the protocol does not
				// offer it.
				offered := offeredDictionary(reply)
				c, err := t.dictionaryFrom(offered)
				if c != nil {
					// Applied after this frame completes - see pendingCodec.
					t.pendingCodec.Store(c)
					// A dictionary that had to be sent is a real cost paid on this
					// connection, so it is recorded and later subtracted from bytes
					// saved. One reused from the cache crossed no wire and is not.
					if !t.fromCache {
						t.dictionaryIn.Add(t.dictWireBytes)
					}
				} else if err != nil {
					// The server named a dictionary this client does not hold or
					// sent one it can not read, so nothing after this frame can be
					// decoded: a protocol error, terminal like any other. The id is
					// forgotten, so a later Connect does not offer it again.
					if t.cache != nil {
						t.cache.forget(offered.Id)
					}
					t.disconnect = &disconnect{Code: disconnectBadProtocol,
						Reason: "dictionary compression: " + err.Error(), Reconnect: false}
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
		if pending := t.pendingCodec.Swap(nil); pending != nil {
			t.codec.Store(pending)
		}
	}
}

// offeredDictionary returns the dictionary a reply carries, if any. Only the
// connect reply carries one: there is no way to replace a dictionary
// mid-connection, since the frame announcing a new one would have to be encoded
// against the old.
func offeredDictionary(reply *protocol.Reply) *protocol.Dictionary {
	if reply.Connect != nil && reply.Connect.Dict != nil {
		return reply.Connect.Dict
	}
	return nil
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
