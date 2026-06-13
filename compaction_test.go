package centrifuge

// Tests for channel compaction and the stale subscribe reply guard. Channel
// compaction is a Centrifugo PRO feature, so it can't be exercised against the
// docker-compose OSS server — these tests use an in-process fake server
// speaking the protobuf protocol which negotiates a numeric channel ID in the
// subscribe reply and then sends pushes carrying the ID instead of the channel
// name, exactly like the real server does when compaction is enabled.

import (
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
	"github.com/gorilla/websocket"
)

type fakeCompactionServer struct {
	srv *httptest.Server

	mu                sync.Mutex
	current           *websocket.Conn
	lastSubscribeFlag int64
	nextChannelID     int64
}

func newFakeCompactionServer(t *testing.T) *fakeCompactionServer {
	t.Helper()
	s := &fakeCompactionServer{nextChannelID: 42}
	upgrader := websocket.Upgrader{Subprotocols: []string{"centrifuge-protobuf"}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.current = conn
		s.mu.Unlock()
		s.readLoop(conn)
	}))
	t.Cleanup(s.close)
	return s
}

func (s *fakeCompactionServer) url() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/connection/websocket"
}

func (s *fakeCompactionServer) close() {
	s.mu.Lock()
	if s.current != nil {
		_ = s.current.Close()
	}
	s.mu.Unlock()
	s.srv.Close()
}

func (s *fakeCompactionServer) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		decoder := protocol.NewProtobufCommandDecoder(data)
		for {
			cmd, err := decoder.Decode()
			if cmd != nil {
				s.handleCommand(conn, cmd)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return
			}
		}
	}
}

func (s *fakeCompactionServer) handleCommand(conn *websocket.Conn, cmd *protocol.Command) {
	switch {
	case cmd.Connect != nil:
		s.sendReply(conn, &protocol.Reply{
			Id:      cmd.Id,
			Connect: &protocol.ConnectResult{Client: "fake-client", Version: "0.0.0", Ping: 25},
		})
	case cmd.Subscribe != nil:
		s.mu.Lock()
		s.lastSubscribeFlag = cmd.Subscribe.Flag
		nextID := s.nextChannelID
		s.mu.Unlock()
		result := &protocol.SubscribeResult{}
		if cmd.Subscribe.Flag&subscriptionFlagChannelCompaction != 0 {
			// Client offered channel compaction — assign a numeric channel ID.
			result.Id = nextID
		}
		s.sendReply(conn, &protocol.Reply{Id: cmd.Id, Subscribe: result})
	case cmd.Unsubscribe != nil:
		s.sendReply(conn, &protocol.Reply{Id: cmd.Id, Unsubscribe: &protocol.UnsubscribeResult{}})
	default:
		if cmd.Id != 0 {
			// Reply to anything else with an empty result to avoid client timeouts.
			s.sendReply(conn, &protocol.Reply{Id: cmd.Id})
		}
	}
}

func (s *fakeCompactionServer) sendReply(conn *websocket.Conn, reply *protocol.Reply) {
	raw, err := reply.MarshalVT()
	if err != nil {
		return
	}
	// Protobuf replies are varint length-delimited on the wire (the encoder
	// itself does not add framing — the real server frames via a DataEncoder).
	data := binary.AppendUvarint(make([]byte, 0, len(raw)+8), uint64(len(raw)))
	data = append(data, raw...)
	// Serialize writes: the read loop and test-driven pushes write concurrently
	// and gorilla connections are not safe for concurrent writers.
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = conn.WriteMessage(websocket.BinaryMessage, data)
}

func (s *fakeCompactionServer) currentConn() *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// sendCompactedPub sends a publication push carrying only the numeric channel ID.
func (s *fakeCompactionServer) sendCompactedPub(id int64, data []byte) {
	s.sendReply(s.currentConn(), &protocol.Reply{
		Push: &protocol.Push{Id: id, Pub: &protocol.Publication{Data: data}},
	})
}

func (s *fakeCompactionServer) sendCompactedJoin(id int64, client string) {
	s.sendReply(s.currentConn(), &protocol.Reply{
		Push: &protocol.Push{Id: id, Join: &protocol.Join{Info: &protocol.ClientInfo{Client: client}}},
	})
}

func (s *fakeCompactionServer) sendCompactedLeave(id int64, client string) {
	s.sendReply(s.currentConn(), &protocol.Reply{
		Push: &protocol.Push{Id: id, Leave: &protocol.Leave{Info: &protocol.ClientInfo{Client: client}}},
	})
}

func waitCh[T any](t *testing.T, ch chan T, label string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", label)
		panic("unreachable")
	}
}

func connectAndSubscribeCompacted(t *testing.T, server *fakeCompactionServer) (*Client, *Subscription, chan SubscribedEvent, chan PublicationEvent) {
	t.Helper()
	client := NewProtobufClient(server.url(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("compacted-channel")
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	pubCh := make(chan PublicationEvent, 8)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	sub.OnPublication(func(e PublicationEvent) { pubCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")
	return client, sub, subscribedCh, pubCh
}

func TestCompactionFlagOfferedAndPushesRoutedByID(t *testing.T) {
	server := newFakeCompactionServer(t)
	_, sub, _, pubCh := connectAndSubscribeCompacted(t, server)

	joinCh := make(chan JoinEvent, 4)
	leaveCh := make(chan LeaveEvent, 4)
	sub.OnJoin(func(e JoinEvent) { joinCh <- e })
	sub.OnLeave(func(e LeaveEvent) { leaveCh <- e })

	server.mu.Lock()
	flag := server.lastSubscribeFlag
	server.mu.Unlock()
	if flag&subscriptionFlagChannelCompaction == 0 {
		t.Fatalf("subscribe request must offer the channel compaction flag, got %d", flag)
	}

	// Publication push with numeric ID only (no channel name).
	server.sendCompactedPub(42, []byte(`{"compacted":true}`))
	pub := waitCh(t, pubCh, "compacted publication")
	if string(pub.Data) != `{"compacted":true}` {
		t.Fatalf("unexpected publication data: %s", pub.Data)
	}

	// Join / leave pushes are compacted the same way.
	server.sendCompactedJoin(42, "other-client")
	join := waitCh(t, joinCh, "compacted join")
	if join.Client != "other-client" {
		t.Fatalf("unexpected join client: %s", join.Client)
	}

	server.sendCompactedLeave(42, "other-client")
	leave := waitCh(t, leaveCh, "compacted leave")
	if leave.Client != "other-client" {
		t.Fatalf("unexpected leave client: %s", leave.ClientInfo.Client)
	}
}

func TestCompactionUnknownIDDropped(t *testing.T) {
	server := newFakeCompactionServer(t)
	_, _, _, pubCh := connectAndSubscribeCompacted(t, server)

	// Unknown ID — must not be delivered anywhere (and must not crash).
	server.sendCompactedPub(99, []byte(`{"stray":true}`))
	// Known ID delivered after the stray one (ordered same socket) proves the
	// client processed and dropped the stray push.
	server.sendCompactedPub(42, []byte(`{"ok":true}`))

	pub := waitCh(t, pubCh, "publication after stray push")
	if string(pub.Data) != `{"ok":true}` {
		t.Fatalf("unexpected publication data: %s", pub.Data)
	}
	select {
	case extra := <-pubCh:
		t.Fatalf("stray push with unknown id was delivered: %s", extra.Data)
	default:
	}
}

func TestCompactionIDDroppedOnUnsubscribeRefreshedOnResubscribe(t *testing.T) {
	server := newFakeCompactionServer(t)
	_, sub, subscribedCh, pubCh := connectAndSubscribeCompacted(t, server)

	unsubscribedCh := make(chan UnsubscribedEvent, 2)
	sub.OnUnsubscribed(func(e UnsubscribedEvent) { unsubscribedCh <- e })
	_ = sub.Unsubscribe()
	waitCh(t, unsubscribedCh, "unsubscribed")

	// Old ID must no longer route to the unsubscribed subscription.
	server.sendCompactedPub(42, []byte(`{"stale":true}`))

	// Resubscribe — the server assigns a fresh ID.
	server.mu.Lock()
	server.nextChannelID = 43
	server.mu.Unlock()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "resubscribed")

	server.sendCompactedPub(43, []byte(`{"fresh":true}`))
	pub := waitCh(t, pubCh, "publication after resubscribe")
	if string(pub.Data) != `{"fresh":true}` {
		t.Fatalf("unexpected publication data: %s", pub.Data)
	}
	select {
	case extra := <-pubCh:
		t.Fatalf("stale push delivered after unsubscribe: %s", extra.Data)
	default:
	}
}

func TestCompactionSameIDReRegisteredAfterReconnect(t *testing.T) {
	// Regression guard (found in the dart port): the client drops the ID
	// registry on teardown (IDs are server-session-scoped), and on reconnect
	// the server commonly assigns the SAME ID to the channel again. The
	// subscription must re-register it even though its own remembered ID is
	// unchanged.
	server := newFakeCompactionServer(t)
	client, _, subscribedCh, pubCh := connectAndSubscribeCompacted(t, server)

	_ = client.Disconnect()
	_ = client.Connect()
	// Subscription auto-resubscribes on reconnect — same ID 42 as before.
	waitCh(t, subscribedCh, "resubscribed after reconnect")

	server.sendCompactedPub(42, []byte(`{"after_reconnect":true}`))
	pub := waitCh(t, pubCh, "publication after reconnect")
	if string(pub.Data) != `{"after_reconnect":true}` {
		t.Fatalf("unexpected publication data: %s", pub.Data)
	}
}

func TestStaleSubscribeReplyDiscarded(t *testing.T) {
	// A subscribe reply produced by a connection that was torn down between the
	// reply arriving and being processed must be discarded — otherwise it flips
	// the subscription to Subscribed while the client reconnects and the
	// post-reconnect resubscribe sweep skips it, stranding the subscription.
	server := newFakeCompactionServer(t)
	client, sub, _, _ := connectAndSubscribeCompacted(t, server)

	generationBeforeTeardown := client.connGeneration.Load()

	disconnectedCh := make(chan DisconnectedEvent, 1)
	client.OnDisconnected(func(e DisconnectedEvent) { disconnectedCh <- e })
	_ = client.Disconnect()
	waitCh(t, disconnectedCh, "disconnected")

	if client.connGeneration.Load() <= generationBeforeTeardown {
		t.Fatal("leaving connected state must bump the connection generation")
	}
	if sub.State() != SubStateSubscribing {
		t.Fatalf("subscription must be subscribing after disconnect, got %s", sub.State())
	}

	// Mechanism check: a reply stamped with the pre-teardown generation is
	// discarded — the subscription stays subscribing.
	res := &protocol.SubscribeResult{}
	sub.moveToSubscribed(res, generationBeforeTeardown)
	if sub.State() != SubStateSubscribing {
		t.Fatalf("stale reply must be discarded, got state %s", sub.State())
	}

	// The same reply stamped with the current generation applies normally.
	sub.moveToSubscribed(res, client.connGeneration.Load())
	if sub.State() != SubStateSubscribed {
		t.Fatalf("reply with current generation must apply, got state %s", sub.State())
	}
}
