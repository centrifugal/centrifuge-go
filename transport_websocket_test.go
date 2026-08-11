package centrifuge

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/centrifugal/protocol"
)

// testDictionaryID is the derivation the protocol fixes for Dictionary.id:
// SHA-256 of the content, first 12 bytes, base64url unpadded. This SDK never
// computes one in earnest - its cache lives in memory, so there is nothing to
// verify - but a test needs an id a server would actually have issued.
func testDictionaryID(dict []byte) string {
	sum := sha256.Sum256(dict)
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// A server that recognises the id a client advertised compresses the connect
// reply itself, since both sides already hold the dictionary. The client must
// therefore be able to decode a frame before any reply has taught it a codec -
// otherwise it can never read the reply that would.
//
// This regressed the moment the server started doing it: the codec was only
// ever installed as a side effect of decoding a reply, which is circular when
// the reply is the compressed thing. The symptom was a permanent disconnect
// with a cached id that was never cleared, so every later attempt failed the
// same way.
func TestWebsocketTransport_DecodesCompressedConnectReply(t *testing.T) {
	dict := []byte(`{"push":{"channel":"","pub":{"data":{"k":null}}}}`)
	id := testDictionaryID(dict)

	cache := newDictionaryCache()
	cache.put(id, dict)
	if cache.advertise() != id {
		t.Fatalf("cache must advertise the id it holds, got %q", cache.advertise())
	}

	tr := &websocketTransport{protocolType: protocol.TypeJSON, cache: cache}
	// Mirrors what the constructor now does before the reader starts.
	if held := cache.advertise(); held != "" {
		if b, ok := cache.get(held); ok {
			tr.codec = protocol.NewDeflateFrameCodec(held, b)
			tr.fromCache = true
		}
	}
	if tr.codec == nil {
		t.Fatal("a client that advertises an id must be able to decode with it")
	}

	// A reply compressed against that dictionary, as a warm server sends it.
	server := protocol.NewDeflateFrameCodec(id, dict)
	raw := []byte(`{"id":1,"connect":{"client":"c1","dict":{"id":"` + id + `"}}}`)
	onWire := server.Compress(nil, raw)
	if onWire[0] != protocol.FrameCodecCompressed {
		t.Fatal("this test is meaningless unless the frame really is compressed")
	}

	got, err := tr.codec.Decompress(nil, onWire, maxDecompressedFrameSize)
	if err != nil {
		t.Fatalf("a warm client must decode the compressed connect reply: %v", err)
	}
	if !bytes.Equal(raw, got) {
		t.Fatalf("decoded frame differs:\n got %s\nwant %s", got, raw)
	}
}
