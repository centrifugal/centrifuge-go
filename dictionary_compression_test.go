package centrifuge

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

// testDictionary is shaped like the frames the tests publish, so compression
// has something to find.
var testDictionary = []byte(`{"push":{"id":42,"pub":{"data":{"type":"quote","symbol":"BTC-USD","bid":`)

func dictionaryServer(t *testing.T) *FakeServer {
	server := NewFakeServer(t)
	server.Dictionary = testDictionary
	server.OnSubscribe = func(_ string, _ *protocol.SubscribeRequest) *protocol.SubscribeResult {
		return &protocol.SubscribeResult{}
	}
	return server
}

func connectCommands(server *FakeServer) []*protocol.ConnectRequest {
	var out []*protocol.ConnectRequest
	for _, cmd := range server.Received() {
		if cmd.Connect != nil {
			out = append(out, cmd.Connect)
		}
	}
	return out
}

func newDictionaryClient(t *testing.T, server *FakeServer) (*Client, chan PublicationEvent) {
	t.Helper()
	client := NewProtobufClient(server.URL(), Config{
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	t.Cleanup(client.Close)
	sub, err := client.NewSubscription("prices")
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	pubCh := make(chan PublicationEvent, 8)
	sub.OnPublication(func(e PublicationEvent) { pubCh <- e })
	_ = sub.Subscribe()
	return client, pubCh
}

// disconnectedEvents delivers the client's disconnected events.
func disconnectedEvents(client *Client) chan DisconnectedEvent {
	ch := make(chan DisconnectedEvent, 4)
	client.OnDisconnected(func(e DisconnectedEvent) { ch <- e })
	return ch
}

// requireBadProtocol waits for the client to stop with bad protocol, the code
// of any protocol error, and a reason starting with want: that is all an
// application gets to see of what went wrong.
func requireBadProtocol(t *testing.T, client *Client, events chan DisconnectedEvent, want string) {
	t.Helper()
	e := waitCh(t, events, "disconnected")
	if e.Code != disconnectBadProtocol || !strings.HasPrefix(e.Reason, want) {
		t.Fatalf("disconnected with %d %q, want %d and a reason starting with %q", e.Code, e.Reason, disconnectBadProtocol, want)
	}
	if state := client.State(); state != StateDisconnected {
		t.Fatalf("a protocol error is terminal, client is %s", state)
	}
}

// requireConnectsWithoutDictionary connects the client again, as an
// application would after a protocol error, and checks it does not offer the
// dictionary that failed and still receives publications.
func requireConnectsWithoutDictionary(t *testing.T, server *FakeServer, client *Client, pubCh chan PublicationEvent) {
	t.Helper()
	if n := len(connectCommands(server)); n != 1 {
		t.Fatalf("a protocol error does not reconnect, got %d connects", n)
	}
	subscribes := func() int {
		n := 0
		for _, cmd := range server.Received() {
			if cmd.Subscribe != nil {
				n++
			}
		}
		return n
	}
	before := subscribes()
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	waitCondition(t, "connect again", func() bool { return len(connectCommands(server)) == 2 })
	if second := connectCommands(server)[1]; second.Dict != "" {
		t.Fatalf("a dictionary that could not be used is not offered again, got %q", second.Dict)
	}
	waitCondition(t, "subscribe", func() bool { return subscribes() == before+1 })
	data := []byte(`{"type":"quote","symbol":"BTC-USD","bid":99}`)
	server.PublishChannel("prices", data)
	if pub := waitCh(t, pubCh, "publication"); !bytes.Equal(pub.Data, data) {
		t.Fatalf("publication data: got %s, want %s", pub.Data, data)
	}
}

// A cold client is sent the dictionary and decodes every frame after the
// connect reply; reconnecting, it advertises the id and is sent nothing but
// that id.
func TestDictionaryCompression_ColdThenWarm(t *testing.T) {
	server := dictionaryServer(t)
	client, pubCh := newDictionaryClient(t, server)
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	waitCondition(t, "subscribe", func() bool { return server.LastSubscribe() != nil })

	data := []byte(`{"type":"quote","symbol":"BTC-USD","bid":101.5}`)
	server.PublishChannel("prices", data)
	if pub := waitCh(t, pubCh, "publication"); !bytes.Equal(pub.Data, data) {
		t.Fatalf("publication data: got %s, want %s", pub.Data, data)
	}

	cold := client.DictionaryCompressionStats()
	if !cold.Accepted || !cold.Active || cold.DictionaryID != testDictionaryID(testDictionary) {
		t.Fatalf("compression not in use: %+v", cold)
	}
	if cold.Frames == 0 || cold.BytesDecompressed <= cold.BytesReceived {
		t.Fatalf("frames were not decompressed: %+v", cold)
	}
	if cold.DictionaryBytes == 0 {
		t.Fatalf("a cold connection pays for the dictionary: %+v", cold)
	}
	if first := connectCommands(server)[0]; first.Flag&connectionFlagDictionaryCompression == 0 || first.Dict != "" {
		t.Fatalf("a cold client advertises the capability and no dictionary, got flag %d dict %q", first.Flag, first.Dict)
	}

	server.CloseConnection()
	waitCondition(t, "reconnect", func() bool { return len(connectCommands(server)) == 2 })
	if second := connectCommands(server)[1]; second.Dict != testDictionaryID(testDictionary) {
		t.Fatalf("a returning client advertises the dictionary it holds, got %q", second.Dict)
	}
	waitCondition(t, "resubscribe", func() bool {
		n := 0
		for _, cmd := range server.Received() {
			if cmd.Subscribe != nil {
				n++
			}
		}
		return n == 2
	})
	server.PublishChannel("prices", data)
	if pub := waitCh(t, pubCh, "publication after reconnect"); !bytes.Equal(pub.Data, data) {
		t.Fatalf("publication data after reconnect: got %s, want %s", pub.Data, data)
	}
	if warm := client.DictionaryCompressionStats(); !warm.Active || warm.DictionaryBytes != 0 {
		t.Fatalf("a warm connection decodes with the dictionary it held and pays nothing for it: %+v", warm)
	}
}

// A dictionary the client can not use leaves it unable to read anything that
// follows: a protocol error, so the client stops rather than misread the stream.
func TestDictionaryCompression_UnusableOfferDisconnects(t *testing.T) {
	id := testDictionaryID(testDictionary)
	tests := []struct {
		name   string
		offer  *protocol.Dictionary
		reason string
	}{
		{"id the client does not hold", &protocol.Dictionary{Id: id},
			"dictionary compression: server named dictionary " + id + ", which this client does not hold"},
		{"content that does not inflate", &protocol.Dictionary{Id: id, Data: []byte("not deflate")},
			"dictionary compression: dictionary " + id + " does not inflate: "},
		{"base64 that does not decode", &protocol.Dictionary{Id: id, DataB64: "%%%"},
			"dictionary compression: dictionary " + id + " is not valid base64: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := dictionaryServer(t)
			var offers atomic.Int32
			server.OfferDictionary = func(req *protocol.ConnectRequest) *protocol.Dictionary {
				if offers.Add(1) == 1 {
					return tt.offer
				}
				return &protocol.Dictionary{Id: id, Data: protocol.DeflateDictionary(testDictionary, 7)}
			}
			client, pubCh := newDictionaryClient(t, server)
			events := disconnectedEvents(client)
			_ = client.Connect()

			requireBadProtocol(t, client, events, tt.reason)
			requireConnectsWithoutDictionary(t, server, client, pubCh)
		})
	}
}

// A server that does not take up compression leaves the client reading plain
// frames, and the stats say so.
func TestDictionaryCompression_NotAccepted(t *testing.T) {
	server := NewFakeServer(t)
	client, pubCh := newDictionaryClient(t, server)
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	waitCondition(t, "subscribe", func() bool { return server.LastSubscribe() != nil })
	server.PublishChannel("prices", []byte(`{}`))
	waitCh(t, pubCh, "publication")
	if s := client.DictionaryCompressionStats(); s.Accepted || s.Active || s.Frames != 0 {
		t.Fatalf("nothing was negotiated: %+v", s)
	}
}

// A frame that does not decode against the dictionary in use is a protocol
// error: the client stops, and forgets the dictionary.
func TestDictionaryCompression_FrameDecodeErrorDisconnects(t *testing.T) {
	server := dictionaryServer(t)
	client, pubCh := newDictionaryClient(t, server)
	events := disconnectedEvents(client)
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	waitCondition(t, "subscribe", func() bool { return server.LastSubscribe() != nil })
	// A compressed marker followed by bytes that are not DEFLATE.
	server.SendFrame([]byte{protocol.FrameCodecCompressed, 0xff, 0xff, 0xff, 0xff})

	requireBadProtocol(t, client, events,
		"dictionary compression: frame does not decode with dictionary "+testDictionaryID(testDictionary)+": ")
	requireConnectsWithoutDictionary(t, server, client, pubCh)
}
