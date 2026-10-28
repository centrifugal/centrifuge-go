package centrifuge

import (
	"bytes"
	"errors"
	"strings"
	"sync"
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

// recordConnecting collects the reasons the client reconnected with; they must
// say what went wrong, since that is all an application gets to see.
func recordConnecting(client *Client) func() []ConnectingEvent {
	var mu sync.Mutex
	var events []ConnectingEvent
	client.OnConnecting(func(e ConnectingEvent) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	})
	return func() []ConnectingEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]ConnectingEvent(nil), events...)
	}
}

// requireReconnectReason waits for a bad protocol reconnect whose reason
// contains want.
func requireReconnectReason(t *testing.T, events func() []ConnectingEvent, want string) {
	t.Helper()
	waitCondition(t, "reconnect reason "+want, func() bool {
		for _, e := range events() {
			if e.Code == disconnectBadProtocol && strings.Contains(e.Reason, want) {
				return true
			}
		}
		return false
	})
}

// recordErrors collects the errors the client reported.
func recordErrors(client *Client) func() []error {
	var mu sync.Mutex
	var errs []error
	client.OnError(func(e ErrorEvent) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, e.Error)
	})
	return func() []error {
		mu.Lock()
		defer mu.Unlock()
		return append([]error(nil), errs...)
	}
}

// requireConnectError waits for a connect error whose text is want: a connect
// reply the client could not use is retried while still connecting, so this is
// where the reason surfaces.
func requireConnectError(t *testing.T, errs func() []error, want string) {
	t.Helper()
	waitCondition(t, "connect error "+want, func() bool {
		for _, err := range errs() {
			var connectErr ConnectError
			if errors.As(err, &connectErr) && strings.Contains(err.Error(), want) {
				return true
			}
		}
		return false
	})
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
// follows, so it must reconnect from scratch rather than misread the stream.
func TestDictionaryCompression_UnusableOfferReconnects(t *testing.T) {
	id := testDictionaryID(testDictionary)
	tests := []struct {
		name   string
		offer  *protocol.Dictionary
		reason string
	}{
		{"id the client does not hold", &protocol.Dictionary{Id: id},
			"dictionary compression: server named dictionary " + id + ", which this client does not hold"},
		{"content that does not inflate", &protocol.Dictionary{Id: id, Data: []byte("not deflate")},
			"dictionary compression: dictionary " + id + " does not inflate"},
		{"base64 that does not decode", &protocol.Dictionary{Id: id, DataB64: "%%%"},
			"dictionary compression: dictionary " + id + " is not valid base64"},
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
			errs := recordErrors(client)
			if err := client.Connect(); err != nil {
				t.Fatal(err)
			}

			requireConnectError(t, errs, tt.reason)
			waitCondition(t, "reconnect after the unusable offer", func() bool { return len(connectCommands(server)) == 2 })
			if second := connectCommands(server)[1]; second.Dict != "" {
				t.Fatalf("a dictionary that could not be used is not advertised again, got %q", second.Dict)
			}

			waitCondition(t, "subscribe", func() bool { return server.LastSubscribe() != nil })
			data := []byte(`{"type":"quote","symbol":"BTC-USD","bid":99}`)
			server.PublishChannel("prices", data)
			if pub := waitCh(t, pubCh, "publication"); !bytes.Equal(pub.Data, data) {
				t.Fatalf("publication data: got %s, want %s", pub.Data, data)
			}
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

// A frame that does not decode against the dictionary in use means the
// dictionary can not be trusted: the client drops it and reconnects without it.
func TestDictionaryCompression_FrameDecodeErrorReconnects(t *testing.T) {
	server := dictionaryServer(t)
	client, pubCh := newDictionaryClient(t, server)
	events := recordConnecting(client)
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	waitCondition(t, "subscribe", func() bool { return server.LastSubscribe() != nil })
	// A compressed marker followed by bytes that are not DEFLATE.
	server.SendFrame([]byte{protocol.FrameCodecCompressed, 0xff, 0xff, 0xff, 0xff})

	requireReconnectReason(t, events,
		"dictionary compression: frame does not decode with dictionary "+testDictionaryID(testDictionary)+": ")

	waitCondition(t, "reconnect after the decode error", func() bool { return len(connectCommands(server)) == 2 })
	if second := connectCommands(server)[1]; second.Dict != "" {
		t.Fatalf("a dictionary that failed to decode is not advertised again, got %q", second.Dict)
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
	data := []byte(`{"type":"quote","symbol":"BTC-USD","bid":98}`)
	server.PublishChannel("prices", data)
	if pub := waitCh(t, pubCh, "publication"); !bytes.Equal(pub.Data, data) {
		t.Fatalf("publication data: got %s, want %s", pub.Data, data)
	}
}
