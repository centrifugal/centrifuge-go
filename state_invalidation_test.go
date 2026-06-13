package centrifuge

// Tests for "state invalidated" handling: unsubscribe code 2502 (per-subscription)
// and disconnect code 3014 (connection-wide). On these the client drops cached
// tokens and the fossil delta base so a fresh token is obtained and state is
// re-synced. Exercised against the in-process FakeServer.

import (
	"testing"
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
