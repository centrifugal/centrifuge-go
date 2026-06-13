package centrifuge

// Tests for channel compaction and the stale subscribe reply guard. Channel
// compaction is a Centrifugo PRO feature, so it can't be exercised against the
// docker-compose OSS server — these tests use the in-process FakeServer (see
// fakeserver_test.go): the subscribe reply negotiates a numeric channel ID and
// subsequent pushes carry the ID instead of the channel name, exactly like the
// real server does when channel compaction is enabled.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

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

// compactionServer returns a FakeServer that negotiates channel compaction:
// it assigns a numeric channel id whenever the client offers the flag. The id
// is taken from *channelID so a test can change it before a resubscribe.
func compactionServer(t *testing.T, channelID *int64) *FakeServer {
	server := NewFakeServer(t)
	atomic.StoreInt64(channelID, 42)
	server.OnSubscribe = func(_ string, req *protocol.SubscribeRequest) *protocol.SubscribeResult {
		result := &protocol.SubscribeResult{}
		if req.Flag&subscriptionFlagChannelCompaction != 0 {
			result.Id = atomic.LoadInt64(channelID)
		}
		return result
	}
	return server
}

func connectAndSubscribeCompacted(t *testing.T, server *FakeServer) (*Client, *Subscription, chan SubscribedEvent, chan PublicationEvent) {
	t.Helper()
	client := NewProtobufClient(server.URL(), Config{})
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
	var channelID int64
	server := compactionServer(t, &channelID)
	_, sub, _, pubCh := connectAndSubscribeCompacted(t, server)

	joinCh := make(chan JoinEvent, 4)
	leaveCh := make(chan LeaveEvent, 4)
	sub.OnJoin(func(e JoinEvent) { joinCh <- e })
	sub.OnLeave(func(e LeaveEvent) { leaveCh <- e })

	if flag := server.LastSubscribe().Flag; flag&subscriptionFlagChannelCompaction == 0 {
		t.Fatalf("subscribe request must offer the channel compaction flag, got %d", flag)
	}

	// Publication push with numeric ID only (no channel name).
	server.PublishID(42, []byte(`{"compacted":true}`))
	pub := waitCh(t, pubCh, "compacted publication")
	if string(pub.Data) != `{"compacted":true}` {
		t.Fatalf("unexpected publication data: %s", pub.Data)
	}

	// Join / leave pushes are compacted the same way.
	server.JoinID(42, "other-client")
	join := waitCh(t, joinCh, "compacted join")
	if join.Client != "other-client" {
		t.Fatalf("unexpected join client: %s", join.Client)
	}

	server.LeaveID(42, "other-client")
	leave := waitCh(t, leaveCh, "compacted leave")
	if leave.Client != "other-client" {
		t.Fatalf("unexpected leave client: %s", leave.Client)
	}
}

func TestCompactionUnknownIDDropped(t *testing.T) {
	var channelID int64
	server := compactionServer(t, &channelID)
	_, _, _, pubCh := connectAndSubscribeCompacted(t, server)

	// Unknown ID — must not be delivered anywhere (and must not crash).
	server.PublishID(99, []byte(`{"stray":true}`))
	// Known ID delivered after the stray one (ordered same socket) proves the
	// client processed and dropped the stray push.
	server.PublishID(42, []byte(`{"ok":true}`))

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
	var channelID int64
	server := compactionServer(t, &channelID)
	_, sub, subscribedCh, pubCh := connectAndSubscribeCompacted(t, server)

	unsubscribedCh := make(chan UnsubscribedEvent, 2)
	sub.OnUnsubscribed(func(e UnsubscribedEvent) { unsubscribedCh <- e })
	_ = sub.Unsubscribe()
	waitCh(t, unsubscribedCh, "unsubscribed")

	// Old ID must no longer route to the unsubscribed subscription.
	server.PublishID(42, []byte(`{"stale":true}`))

	// Resubscribe — the server assigns a fresh ID.
	atomic.StoreInt64(&channelID, 43)
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "resubscribed")

	server.PublishID(43, []byte(`{"fresh":true}`))
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
	var channelID int64
	server := compactionServer(t, &channelID)
	client, _, subscribedCh, pubCh := connectAndSubscribeCompacted(t, server)

	_ = client.Disconnect()
	_ = client.Connect()
	// Subscription auto-resubscribes on reconnect — same ID 42 as before.
	waitCh(t, subscribedCh, "resubscribed after reconnect")

	server.PublishID(42, []byte(`{"after_reconnect":true}`))
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
	var channelID int64
	server := compactionServer(t, &channelID)
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
