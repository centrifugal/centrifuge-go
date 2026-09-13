package centrifuge

// Tests for "state invalidated" handling: unsubscribe code 2502 (per-subscription)
// and disconnect code 3014 (connection-wide). On these the client drops cached
// tokens and the fossil delta base so a fresh token is obtained and state is
// re-synced. Exercised against the in-process FakeServer.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

func TestInvalidateStateClearsTokenAndDeltaBase(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		Token:    "sub-token",
		GetToken: func(SubscriptionTokenEvent) (string, error) { return "new-sub-token", nil },
	})
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

func TestInvalidateStateKeepsTokenWithoutGetToken(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{Token: "sub-token"})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	sub.prevData = []byte("stale-delta-base")

	sub.invalidateState()

	if sub.token != "sub-token" {
		t.Fatalf("a token without GetToken can't be replaced and must be kept, got %q", sub.token)
	}
	if sub.prevData != nil {
		t.Fatalf("delta base must be cleared, got %q", sub.prevData)
	}
}

func TestInvalidateConnectionStateClearsTokenAndAllSubs(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{
		Token:    "conn-token",
		GetToken: func(ConnectionTokenEvent) (string, error) { return "new-conn-token", nil },
	})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		Token:    "sub-token",
		GetToken: func(SubscriptionTokenEvent) (string, error) { return "new-sub-token", nil },
	})
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

func TestStateInvalidationWithoutGetTokenReconnectsWithCurrentToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
	}{
		{name: "static token", token: "static-token"},
		{name: "anonymous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewFakeServer(t)
			client := NewProtobufClient(server.URL(), Config{
				Token:             tc.token,
				MinReconnectDelay: 10 * time.Millisecond,
				MaxReconnectDelay: 20 * time.Millisecond,
			})
			t.Cleanup(client.Close)
			errCh := make(chan error, 4)
			client.OnError(func(e ErrorEvent) {
				select {
				case errCh <- e.Error:
				default:
				}
			})
			disconnectedCh := make(chan DisconnectedEvent, 1)
			client.OnDisconnected(func(e DisconnectedEvent) { disconnectedCh <- e })

			sub, err := client.NewSubscription("ch", SubscriptionConfig{Token: "sub-token"})
			if err != nil {
				t.Fatalf("new subscription: %v", err)
			}
			subscribedCh := make(chan SubscribedEvent, 4)
			sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

			_ = client.Connect()
			_ = sub.Subscribe()
			waitCh(t, subscribedCh, "subscribed")

			server.DisconnectPush(disconnectedStateInvalidated, "state invalidated")
			select {
			case <-subscribedCh:
			case e := <-disconnectedCh:
				t.Fatalf("client without GetToken disconnected after 3014 (code %d, %q) instead of reconnecting", e.Code, e.Reason)
			case <-time.After(3 * time.Second):
				t.Fatal("timeout waiting for resubscribe after 3014")
			}
			received := server.Received()
			for i := len(received) - 1; i >= 0; i-- {
				if received[i].Connect != nil {
					if token := received[i].Connect.Token; token != tc.token {
						t.Fatalf("reconnect after 3014 must use the client's token %q, got %q", tc.token, token)
					}
					break
				}
			}
			if token := server.LastSubscribe().Token; token != "sub-token" {
				t.Fatalf("resubscribe after 3014 must use the subscription's token, got %q", token)
			}
			select {
			case err := <-errCh:
				t.Fatalf("unexpected error: %v", err)
			default:
			}
		})
	}
}

func TestUnsubscribe2502InvalidatesAndResubscribes(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	// A token without GetToken can't be replaced, so it's kept.
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
	if token != "sub-token" {
		t.Fatalf("a token without GetToken must be kept after 2502, got %q", token)
	}
	if resubscribeToken := server.LastSubscribe().Token; resubscribeToken != "sub-token" {
		t.Fatalf("resubscribe after 2502 must send the kept token, got %q", resubscribeToken)
	}
	if prevData != nil {
		t.Fatalf("delta base must be cleared by 2502, got %q", prevData)
	}
}

// blockFirstCall returns a channel signalled when the first call starts and a
// release function that lets it return; release also runs on cleanup.
func blockFirstCall(t *testing.T) (chan struct{}, <-chan struct{}, func()) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var released atomic.Bool
	releaseFn := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(releaseFn)
	return started, release, releaseFn
}

// invalidateWhilePending sends a 2502 for ch once the subscription's first
// GetToken or GetState call started, and lets that call return afterwards.
func invalidateWhilePending(t *testing.T, server *FakeServer, sub *Subscription, started chan struct{}, release func()) {
	t.Helper()
	waitCh(t, started, "the first call")
	server.UnsubscribePush(sub.Channel, unsubscribedStateInvalidated, "state invalidated")
	waitCondition(t, "the state invalidation", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.epoch == stateInvalidatedEpoch
	})
	release()
}

func TestUnsubscribe2502DiscardsPendingSubscriptionToken(t *testing.T) {
	server := NewFakeServer(t)
	client := connectFakeClient(t, server, Config{})
	started, release, releaseFn := blockFirstCall(t)
	var calls atomic.Int32
	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetToken: func(SubscriptionTokenEvent) (string, error) {
			if calls.Add(1) == 1 {
				started <- struct{}{}
				<-release
				return "token-before-invalidation", nil
			}
			return "token-after-invalidation", nil
		},
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = sub.Subscribe()

	invalidateWhilePending(t, server, sub, started, releaseFn)
	waitCh(t, subscribedCh, "subscribed")
	if token := server.LastSubscribe().Token; token != "token-after-invalidation" {
		t.Fatalf("the subscribe must use a token obtained after the invalidation, got %q", token)
	}
}

func TestUnsubscribe2502DiscardsPendingGetState(t *testing.T) {
	server := NewFakeServer(t)
	client := connectFakeClient(t, server, Config{})
	started, release, releaseFn := blockFirstCall(t)
	var calls atomic.Int32
	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetState: func(SubscriptionGetStateEvent) (StreamPosition, error) {
			if calls.Add(1) == 1 {
				started <- struct{}{}
				<-release
				return StreamPosition{Offset: 5, Epoch: "before"}, nil
			}
			return StreamPosition{Offset: 7, Epoch: "after"}, nil
		},
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = sub.Subscribe()

	invalidateWhilePending(t, server, sub, started, releaseFn)
	waitCh(t, subscribedCh, "subscribed")
	if req := server.LastSubscribe(); !req.Recover || req.Offset != 7 || req.Epoch != "after" {
		t.Fatalf("the subscribe must use the position GetState returned after the invalidation, got recover=%v offset=%d epoch=%q", req.Recover, req.Offset, req.Epoch)
	}
}

func TestResubscribePushAfterUnsubscribeKeepsUnsubscribed(t *testing.T) {
	for _, tc := range []struct {
		name string
		code uint32
	}{
		{name: "resubscribe", code: 2500},
		{name: "state invalidated", code: unsubscribedStateInvalidated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewFakeServer(t)
			client := connectFakeClient(t, server, Config{})
			sub, err := client.NewSubscription("ch")
			if err != nil {
				t.Fatalf("new subscription: %v", err)
			}
			subscribedCh := make(chan SubscribedEvent, 4)
			sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
			_ = sub.Subscribe()
			waitCh(t, subscribedCh, "subscribed")

			// The application unsubscribes while a server unsubscribe push that
			// asks to resubscribe is already on its way.
			if err := sub.Unsubscribe(); err != nil {
				t.Fatal(err)
			}
			server.UnsubscribePush("ch", tc.code, "resubscribe")

			// The server writes the push before this subscribe's reply, so the
			// client has handled the push once the marker is subscribed.
			marker, err := client.NewSubscription("marker")
			if err != nil {
				t.Fatalf("new subscription: %v", err)
			}
			markerCh := make(chan SubscribedEvent, 1)
			marker.OnSubscribed(func(e SubscribedEvent) { markerCh <- e })
			_ = marker.Subscribe()
			waitCh(t, markerCh, "marker subscribed")

			if state := sub.State(); state != SubStateUnsubscribed {
				t.Fatalf("a resubscribe push handled after Unsubscribe brought the subscription back: state %s", state)
			}
		})
	}
}

func TestTeardownAfterUnsubscribeKeepsUnsubscribed(t *testing.T) {
	server := NewFakeServer(t)
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)
	sub, err := client.NewSubscription("ch")
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	_ = sub.Subscribe()
	// A teardown collects the subscriptions that aren't unsubscribed and moves
	// them to subscribing after releasing the client lock: an Unsubscribe in
	// between must not be undone.
	if err := sub.Unsubscribe(); err != nil {
		t.Fatal(err)
	}
	sub.moveToSubscribing(subscribingTransportClosed, "transport closed")
	if state := sub.State(); state != SubStateUnsubscribed {
		t.Fatalf("a teardown undid Unsubscribe: state %s", state)
	}
}

func TestStateInvalidationDiscardsPendingRefreshToken(t *testing.T) {
	server := NewFakeServer(t)
	server.OnSubscribe = func(string, *protocol.SubscribeRequest) *protocol.SubscribeResult {
		return &protocol.SubscribeResult{Expires: true, Ttl: 1}
	}
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.SubRefresh != nil {
			return &protocol.Reply{Id: cmd.Id, SubRefresh: &protocol.SubRefreshResult{}}
		}
		return nil
	}
	client := connectFakeClient(t, server, Config{})
	refreshStarted := make(chan struct{}, 1)
	refreshReturned := make(chan struct{}, 1)
	release := make(chan struct{})
	var released atomic.Bool
	releaseRefresh := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(releaseRefresh)
	var calls atomic.Int32
	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetToken: func(SubscriptionTokenEvent) (string, error) {
			if calls.Add(1) != 2 {
				return "subscribe-token", nil
			}
			refreshStarted <- struct{}{}
			<-release
			refreshReturned <- struct{}{}
			return "refreshed-before-invalidation", nil
		},
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	// The state is invalidated while the refresh's GetToken runs, before the
	// subscription moves to subscribing (as between invalidateState and
	// moveToSubscribing on a 2502 or a 3014).
	waitCh(t, refreshStarted, "the token refresh")
	sub.invalidateState()
	releaseRefresh()
	waitCh(t, refreshReturned, "the refresh's GetToken to return")
	time.Sleep(100 * time.Millisecond)

	sub.mu.Lock()
	token := sub.token
	sub.mu.Unlock()
	if token == "refreshed-before-invalidation" {
		t.Fatal("the token of a refresh started before the state invalidation was cached")
	}
	for _, cmd := range server.Received() {
		if cmd.SubRefresh != nil {
			t.Fatal("a refresh started before the state invalidation was sent")
		}
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
