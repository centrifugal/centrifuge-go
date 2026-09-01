package centrifuge

// Tests for "state invalidated" handling: unsubscribe code 2502 (per-subscription)
// and disconnect code 3014 (connection-wide). On these the client drops cached
// tokens and the fossil delta base so a fresh token is obtained and state is
// re-synced. Exercised against the in-process FakeServer.

import (
	"testing"

	"github.com/centrifugal/protocol"
)

func TestInvalidateStateClearsTokenAndDeltaBase(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{Token: "sub-token"})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	sub.prevData = []byte("stale-delta-base")
	sub.offset = 10
	sub.epoch = "e1"
	sub.recover = true

	sub.invalidateState()

	if sub.token != "" {
		t.Fatalf("token must be cleared, got %q", sub.token)
	}
	if sub.prevData != nil {
		t.Fatalf("delta base must be cleared, got %q", sub.prevData)
	}
	// Recovery position is reset to a deliberately unrecoverable one: recover
	// stays true with the sentinel epoch, so the resubscribe reports
	// WasRecovering=true, Recovered=false.
	if !sub.recover || sub.offset != 0 || sub.epoch != stateInvalidatedEpoch {
		t.Fatalf("recovery position must be reset to the unrecoverable sentinel, got offset=%d epoch=%q recover=%v", sub.offset, sub.epoch, sub.recover)
	}
}

func TestInvalidateConnectionStateClearsTokenAndAllSubs(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{Token: "conn-token"})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{Token: "sub-token"})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	sub.prevData = []byte("stale")

	client.invalidateConnectionState()

	client.mu.Lock()
	connToken := client.token
	refreshRequired := client.refreshRequired
	client.mu.Unlock()
	if connToken != "" {
		t.Fatalf("connection token must be cleared, got %q", connToken)
	}
	if !refreshRequired {
		t.Fatal("refreshRequired must be set so a fresh token is fetched on reconnect")
	}
	if sub.token != "" || sub.prevData != nil {
		t.Fatalf("subscription state must be invalidated, got token=%q prevData=%q", sub.token, sub.prevData)
	}
}

func TestInvalidateConnectionStateResetsServerSubRecoveryPosition(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{Token: "conn-token"})
	t.Cleanup(client.Close)

	client.mu.Lock()
	client.serverSubs["ch"] = &serverSub{
		Recoverable: true,
		Offset:      10,
		Epoch:       "e1",
	}
	client.mu.Unlock()

	client.invalidateConnectionState()

	client.mu.Lock()
	sub := client.serverSubs["ch"]
	client.mu.Unlock()
	if sub == nil {
		t.Fatal("server-side subscription must still be present")
	}
	// Recovery position is reset to the same unrecoverable sentinel used for
	// client-side subscriptions, so the next connect request doesn't ask the
	// server to recover from a position that predates the invalidation.
	// Recoverable is left untouched.
	if !sub.Recoverable || sub.Offset != 0 || sub.Epoch != stateInvalidatedEpoch {
		t.Fatalf("server-side sub recovery position must be reset to the unrecoverable sentinel, got offset=%d epoch=%q recoverable=%v", sub.Offset, sub.Epoch, sub.Recoverable)
	}
}

func TestDisconnect3014ResetsServerSubRecoveryPositionOnWire(t *testing.T) {
	// End-to-end companion to TestInvalidateConnectionStateResetsServerSubRecoveryPosition:
	// that test proves invalidateConnectionState mutates serverSubs in isolation, this one
	// proves the reset value actually reaches the wire on the reconnect's Connect request.
	server := NewFakeServer(t)
	server.ConnectResult = &protocol.ConnectResult{
		Client: "fake-client",
		Subs: map[string]*protocol.SubscribeResult{
			"news": {Recoverable: true, Epoch: "server-epoch", Offset: 5},
		},
	}
	client := NewProtobufClient(server.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) { return "c1", nil },
	})
	t.Cleanup(client.Close)

	subscribedCh := make(chan ServerSubscribedEvent, 4)
	client.OnSubscribed(func(e ServerSubscribedEvent) { subscribedCh <- e })

	_ = client.Connect()
	waitCh(t, subscribedCh, "server-side subscribed")

	lastConnect := func() *protocol.ConnectRequest {
		received := server.Received()
		for i := len(received) - 1; i >= 0; i-- {
			if received[i].Connect != nil {
				return received[i].Connect
			}
		}
		return nil
	}
	if sub := lastConnect().Subs["news"]; sub != nil {
		t.Fatalf("initial connect must carry no server subs to recover, got %+v", sub)
	}

	server.DisconnectPush(disconnectedStateInvalidated, "state invalidated")
	waitCh(t, subscribedCh, "resubscribed after reconnect")

	sub := lastConnect().Subs["news"]
	if sub == nil {
		t.Fatal("reconnect must request recovery for the server-side sub")
	}
	if !sub.Recover || sub.Offset != 0 || sub.Epoch != stateInvalidatedEpoch {
		t.Fatalf("reconnect must not carry the pre-invalidation offset/epoch, got recover=%v offset=%d epoch=%q", sub.Recover, sub.Offset, sub.Epoch)
	}
}

func TestUnsubscribe2502InvalidatesAndResubscribes(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	// Initial token, no GetToken — so after invalidation the token stays empty
	// (nothing repopulates it), letting us observe the clear deterministically.
	sub, err := client.NewSubscription("ch", SubscriptionConfig{Token: "sub-token"})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	sub.mu.Lock()
	sub.prevData = []byte("stale-delta-base")
	sub.mu.Unlock()

	// Server sends "state invalidated" unsubscribe — sub must re-subscribe.
	server.UnsubscribePush("ch", unsubscribedStateInvalidated, "state invalidated")
	waitCh(t, subscribedCh, "resubscribed after 2502")

	sub.mu.Lock()
	token, prevData := sub.token, sub.prevData
	sub.mu.Unlock()
	if token != "" {
		t.Fatalf("subscription token must be cleared by 2502, got %q", token)
	}
	if prevData != nil {
		t.Fatalf("delta base must be cleared by 2502, got %q", prevData)
	}
}

func TestUnsubscribeBelow2500DoesNotInvalidate(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{Token: "sub-token"})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	unsubscribedCh := make(chan UnsubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	sub.OnUnsubscribed(func(e UnsubscribedEvent) { unsubscribedCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	// A code < 2500 fully unsubscribes (no resubscribe, no invalidation path).
	server.UnsubscribePush("ch", 2000, "server unsubscribe")
	ev := waitCh(t, unsubscribedCh, "unsubscribed")
	if ev.Code != 2000 {
		t.Fatalf("unexpected unsubscribe code: %d", ev.Code)
	}
}
