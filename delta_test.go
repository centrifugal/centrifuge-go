package centrifuge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/centrifugal/fdelta"
	"github.com/centrifugal/protocol"
)

// deltaSub returns a subscription with delta already negotiated, ready for
// applyDeltaLocked.
func deltaSub(t testing.TB, client *Client, name string) *Subscription {
	t.Helper()
	sub, err := client.NewSubscription(name, SubscriptionConfig{Delta: DeltaTypeFossil})
	if err != nil {
		t.Fatal(err)
	}
	sub.deltaNegotiated = true
	return sub
}

// TestApplyMalformedDeltaNeverPanics is what lets applyDeltaLocked call
// fdelta.Apply directly. The previous library could index past the end of a
// malformed delta, so every call was wrapped in a deferred recover; fdelta
// validates a delta completely before allocating and returns an error instead.
//
// A panic here fails the test by crashing it, so the assertion is that every
// one of these returns an ordinary error.
func TestApplyMalformedDeltaNeverPanics(t *testing.T) {
	client := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer client.Close()

	base := []byte(`{"channel":"news","seq":1,"text":"the quick brown fox"}`)
	target := []byte(`{"channel":"news","seq":2,"text":"the quick brown fox"}`)
	valid := fdelta.Create(base, target)
	if len(valid) == 0 {
		t.Fatal("empty delta")
	}

	var deltas [][]byte
	add := func(d []byte) { deltas = append(deltas, d) }

	// Hand-written shapes that exercise the parser's edges.
	for _, s := range []string{
		"", "\n", ";", "0;", "3\n", "3\n3@0,", "3\n3:a", "3\n0@0,", "~~~~~~\n",
		"999999999999\n", "-1\n", "3\n3@999999999,", "3\n3@0,0;", "abc",
	} {
		add([]byte(s))
	}

	// Every truncation of a valid delta.
	for i := range len(valid) {
		add(bytes.Clone(valid[:i]))
	}

	// Every single-byte corruption at a few positions, plus random mutations.
	for i := range len(valid) {
		for _, b := range []byte{0x00, 0xff, '~', '\n', ':', '@', ','} {
			d := bytes.Clone(valid)
			d[i] = b
			add(d)
		}
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		d := bytes.Clone(valid)
		for range 1 + r.IntN(4) {
			d[r.IntN(len(d))] = byte(r.IntN(256))
		}
		add(d)
	}

	sub := deltaSub(t, client, "malformed")
	var errs, applied int
	for _, d := range deltas {
		sub.prevData = base
		_, err := sub.applyDeltaLocked(&protocol.Publication{Data: d, Delta: true}, PublicationEvent{})
		if err != nil {
			errs++
			continue
		}
		// A mutation can still be a valid delta; it just must not be a panic.
		applied++
	}
	t.Logf("%d malformed deltas rejected with an error, %d still valid, 0 panics", errs, applied)
	if errs == 0 {
		t.Fatal("no delta was rejected, the corpus is not exercising the parser")
	}
}

// TestApplyDeltaRoundTrip drives the two paths applyDeltaLocked takes -- raw
// bytes for protobuf, a JSON string for JSON -- over a chain of publications,
// the way a subscription receives them.
func TestApplyDeltaRoundTrip(t *testing.T) {
	jsonClient := NewJsonClient("ws://localhost:8000/connection/websocket", Config{})
	defer jsonClient.Close()
	protobufClient := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer protobufClient.Close()

	// A chain of payloads, each a small edit of the one before, including
	// multibyte text so the JSON path carries more than ASCII.
	payloads := [][]byte{
		[]byte(`{"seq":1,"text":"hello","note":"первое"}`),
		[]byte(`{"seq":2,"text":"hello","note":"первое"}`),
		[]byte(`{"seq":3,"text":"hello world","note":"второе"}`),
		[]byte(`{"seq":4,"text":"hello world","note":"второе","extra":"` + string(bytes.Repeat([]byte("x"), 512)) + `"}`),
		[]byte(`{"seq":5,"text":"goodbye","note":"третье"}`),
	}

	for _, tc := range []struct {
		name   string
		client *Client
		isJSON bool
	}{
		{"protobuf", protobufClient, false},
		{"json", jsonClient, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := deltaSub(t, tc.client, "roundtrip-"+tc.name)
			sub.prevData = payloads[0]

			for i := 1; i < len(payloads); i++ {
				prev, next := payloads[i-1], payloads[i]
				delta := fdelta.Create(prev, next)
				if len(delta) == 0 {
					t.Fatalf("step %d: empty delta", i)
				}
				data := delta
				if tc.isJSON {
					// The server sends a delta as a JSON string.
					encoded, err := json.Marshal(string(delta))
					if err != nil {
						t.Fatalf("step %d: marshal: %v", i, err)
					}
					data = encoded
				}
				event, err := sub.applyDeltaLocked(&protocol.Publication{Data: data, Delta: true}, PublicationEvent{})
				if err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
				if !bytes.Equal(event.Data, next) {
					t.Fatalf("step %d: got %q, want %q", i, event.Data, next)
				}
				if !bytes.Equal(sub.prevData, next) {
					t.Fatalf("step %d: base not advanced", i)
				}
			}
		})
	}
}

// TestApplyDeltaWrongBaseIsRejected covers the case a client hits when its
// base has drifted from the server's: the delta is well formed but was built
// against different bytes, and must not be delivered as data.
func TestApplyDeltaWrongBaseIsRejected(t *testing.T) {
	client := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer client.Close()

	base := []byte(`{"seq":1,"text":"the quick brown fox jumps over the lazy dog"}`)
	target := []byte(`{"seq":2,"text":"the quick brown fox jumps over the lazy dog"}`)
	delta := fdelta.Create(base, target)

	sub := deltaSub(t, client, "wrong-base")
	sub.prevData = []byte(`{"seq":9,"text":"something else entirely, of a different length"}`)

	if _, err := sub.applyDeltaLocked(&protocol.Publication{Data: delta, Delta: true}, PublicationEvent{}); err == nil {
		t.Fatal("expected an error applying a delta to the wrong base")
	}
}

func benchmarkDeltaPayload(n int) (prev, next []byte) {
	var buf bytes.Buffer
	buf.WriteString(`{"items":[`)
	for i := 0; buf.Len() < n; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"id":%d,"name":"item-%d","status":"idle","value":%d.%02d}`, i, i, i*7%1000, i%100)
	}
	buf.WriteString(`]}`)
	prev = buf.Bytes()
	next = bytes.Replace(prev, []byte(`"status":"idle"`), []byte(`"status":"busy"`), 1)
	return prev, next
}

// BenchmarkApplyDelta measures what a subscribed client does per delta
// publication, across payload sizes and both protocol paths.
func BenchmarkApplyDelta(b *testing.B) {
	jsonClient := NewJsonClient("ws://localhost:8000/connection/websocket", Config{})
	defer jsonClient.Close()
	protobufClient := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer protobufClient.Close()

	for _, size := range []int{1 << 10, 4 << 10, 16 << 10, 64 << 10} {
		prev, next := benchmarkDeltaPayload(size)
		delta := fdelta.Create(prev, next)
		jsonDelta, err := json.Marshal(string(delta))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("%dKiB", len(next)>>10), func(b *testing.B) {
			for _, tc := range []struct {
				name   string
				client *Client
				data   []byte
			}{
				{"protobuf", protobufClient, delta},
				{"json", jsonClient, jsonDelta},
			} {
				b.Run(tc.name, func(b *testing.B) {
					sub := deltaSub(b, tc.client, fmt.Sprintf("bench-%s-%d", tc.name, size))
					pub := &protocol.Publication{Data: tc.data, Delta: true}
					b.SetBytes(int64(len(next)))
					b.ReportAllocs()
					for b.Loop() {
						sub.prevData = prev
						if _, err := sub.applyDeltaLocked(pub, PublicationEvent{}); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
