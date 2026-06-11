package centrifuge

// Integration tests for SubscriptionConfig.GetState. These mirror the getState
// tests in centrifuge-js and centrifuge-dart and require the docker-compose
// Centrifugo (>= 6.8.0) running on localhost:8000.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestJsonClient() *Client {
	return NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
}

func waitSubscribed(t *testing.T, ch chan SubscribedEvent) SubscribedEvent {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for subscribed event")
		return SubscribedEvent{}
	}
}

func TestGetStateCalledOnInitialSubscribe(t *testing.T) {
	publisher := newTestJsonClient()
	defer publisher.Close()
	_ = publisher.Connect()

	channel := "test_getstate_" + randString(8)

	// Publish 3 messages BEFORE subscribing.
	for i := 1; i <= 3; i++ {
		if _, err := publisher.Publish(context.Background(), channel, []byte(`{"i":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("publish error: %v", err)
		}
	}

	client := newTestJsonClient()
	defer client.Close()
	_ = client.Connect()

	// GetState returns zero position — recovery delivers all 3 publications.
	var getStateCalls int64
	sub, err := client.NewSubscription(channel, SubscriptionConfig{
		GetState: func(e SubscriptionGetStateEvent) (StreamPosition, error) {
			atomic.AddInt64(&getStateCalls, 1)
			return StreamPosition{}, nil
		},
	})
	if err != nil {
		t.Fatalf("error on new subscription: %v", err)
	}

	subscribedCh := make(chan SubscribedEvent, 1)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	pubCh := make(chan PublicationEvent, 3)
	sub.OnPublication(func(e PublicationEvent) { pubCh <- e })

	_ = sub.Subscribe()
	waitSubscribed(t, subscribedCh)

	for i := 1; i <= 3; i++ {
		select {
		case <-pubCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for recovered publication %d", i)
		}
	}
	if calls := atomic.LoadInt64(&getStateCalls); calls != 1 {
		t.Fatalf("expected 1 GetState call, got %d", calls)
	}
}

func TestGetStateNotCalledWhenRecoverySucceeds(t *testing.T) {
	publisher := newTestJsonClient()
	defer publisher.Close()
	_ = publisher.Connect()

	channel := "test_getstate_rec_" + randString(8)

	client := newTestJsonClient()
	defer client.Close()
	_ = client.Connect()

	var getStateCalls int64
	sub, err := client.NewSubscription(channel, SubscriptionConfig{
		GetState: func(e SubscriptionGetStateEvent) (StreamPosition, error) {
			atomic.AddInt64(&getStateCalls, 1)
			return StreamPosition{}, nil
		},
	})
	if err != nil {
		t.Fatalf("error on new subscription: %v", err)
	}

	subscribedCh := make(chan SubscribedEvent, 2)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	pubCh := make(chan PublicationEvent, 2)
	sub.OnPublication(func(e PublicationEvent) { pubCh <- e })

	_ = sub.Subscribe()
	waitSubscribed(t, subscribedCh)
	if calls := atomic.LoadInt64(&getStateCalls); calls != 1 {
		t.Fatalf("expected 1 GetState call after initial subscribe, got %d", calls)
	}

	// Disconnect, publish while away, reconnect — SDK has a saved position
	// and recovery succeeds, so GetState must NOT be called again.
	_ = client.Disconnect()

	for i := 1; i <= 2; i++ {
		if _, err := publisher.Publish(context.Background(), channel, []byte(`{"i":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("publish error: %v", err)
		}
	}

	_ = client.Connect()
	e := waitSubscribed(t, subscribedCh)
	if !e.Recovered {
		t.Fatalf("expected successful recovery on reconnect")
	}

	for i := 1; i <= 2; i++ {
		select {
		case <-pubCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("timeout waiting for recovered publication %d", i)
		}
	}
	if calls := atomic.LoadInt64(&getStateCalls); calls != 1 {
		t.Fatalf("GetState must not be called when recovery succeeds, got %d calls", calls)
	}
}

func TestGetStateErrorRetried(t *testing.T) {
	client := newTestJsonClient()
	defer client.Close()
	_ = client.Connect()

	channel := "test_getstate_err_" + randString(8)

	// First GetState call fails, second succeeds.
	var getStateCalls int64
	sub, err := client.NewSubscription(channel, SubscriptionConfig{
		MinResubscribeDelay: 50 * time.Millisecond,
		MaxResubscribeDelay: 50 * time.Millisecond,
		GetState: func(e SubscriptionGetStateEvent) (StreamPosition, error) {
			if atomic.AddInt64(&getStateCalls, 1) == 1 {
				return StreamPosition{}, errors.New("simulated DB failure")
			}
			return StreamPosition{}, nil
		},
	})
	if err != nil {
		t.Fatalf("error on new subscription: %v", err)
	}

	errCh := make(chan error, 8)
	sub.OnError(func(e SubscriptionErrorEvent) { errCh <- e.Error })
	subscribedCh := make(chan SubscribedEvent, 1)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	_ = sub.Subscribe()

	// First GetState fails → error emitted → resubscribe scheduled with
	// backoff. Second GetState succeeds → subscribe completes.
	waitSubscribed(t, subscribedCh)

	if calls := atomic.LoadInt64(&getStateCalls); calls < 2 {
		t.Fatalf("expected at least 2 GetState calls, got %d", calls)
	}
	select {
	case err := <-errCh:
		var getStateErr SubscriptionGetStateError
		if !errors.As(err, &getStateErr) {
			t.Fatalf("expected SubscriptionGetStateError, got %T (%v)", err, err)
		}
		if !strings.Contains(err.Error(), "simulated DB failure") {
			t.Fatalf("unexpected error message: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for subscription error event")
	}
}

func TestGetStatePersistentFailureKeepsRetrying(t *testing.T) {
	client := newTestJsonClient()
	defer client.Close()
	_ = client.Connect()

	channel := "test_getstate_persist_" + randString(8)

	var getStateCalls int64
	sub, err := client.NewSubscription(channel, SubscriptionConfig{
		MinResubscribeDelay: 50 * time.Millisecond,
		MaxResubscribeDelay: 50 * time.Millisecond,
		GetState: func(e SubscriptionGetStateEvent) (StreamPosition, error) {
			atomic.AddInt64(&getStateCalls, 1)
			return StreamPosition{}, errors.New("always fails")
		},
	})
	if err != nil {
		t.Fatalf("error on new subscription: %v", err)
	}

	_ = sub.Subscribe()

	// Wait for several retry cycles.
	time.Sleep(500 * time.Millisecond)

	if calls := atomic.LoadInt64(&getStateCalls); calls <= 2 {
		t.Fatalf("expected more than 2 GetState calls, got %d", calls)
	}
	if state := sub.State(); state != SubStateSubscribing {
		t.Fatalf("expected subscription to stay in subscribing state, got %s", state)
	}
	_ = sub.Unsubscribe()
}

func TestGetStateCalledAgainOnUnrecoverablePosition(t *testing.T) {
	// Uses "smallhistory" namespace with history_size=2. After publishing
	// enough to evict old entries, reconnecting from an old position triggers
	// error 112 (unrecoverable position) because the subscribe request carries
	// the reject_unrecovered flag. The SDK must then call GetState again to
	// reload app state instead of delivering recovered=false on an active
	// subscription.
	publisher := newTestJsonClient()
	defer publisher.Close()
	_ = publisher.Connect()

	channel := "smallhistory:test_getstate_" + randString(8)

	client := newTestJsonClient()
	defer client.Close()
	_ = client.Connect()

	// Simulate a real app: GetState reads current stream position from the
	// backend (here — via channel history top position).
	var getStateCalls int64
	sub, err := client.NewSubscription(channel, SubscriptionConfig{
		MinResubscribeDelay: 50 * time.Millisecond,
		MaxResubscribeDelay: 50 * time.Millisecond,
		GetState: func(e SubscriptionGetStateEvent) (StreamPosition, error) {
			atomic.AddInt64(&getStateCalls, 1)
			res, err := publisher.History(context.Background(), e.Channel)
			if err != nil {
				return StreamPosition{}, err
			}
			return StreamPosition{Offset: res.Offset, Epoch: res.Epoch}, nil
		},
	})
	if err != nil {
		t.Fatalf("error on new subscription: %v", err)
	}

	subscribedCh := make(chan SubscribedEvent, 2)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	pubCh := make(chan PublicationEvent, 8)
	sub.OnPublication(func(e PublicationEvent) { pubCh <- e })

	_ = sub.Subscribe()
	waitSubscribed(t, subscribedCh)
	if calls := atomic.LoadInt64(&getStateCalls); calls != 1 {
		t.Fatalf("expected 1 GetState call after initial subscribe, got %d", calls)
	}

	// Disconnect, then publish enough messages to push the stream beyond
	// recovery (history_size=2, so 5 messages evict old entries).
	_ = client.Disconnect()

	for i := 1; i <= 5; i++ {
		if _, err := publisher.Publish(context.Background(), channel, []byte(`{"i":`+strconv.Itoa(i)+`}`)); err != nil {
			t.Fatalf("publish error: %v", err)
		}
	}

	// Reconnect — SDK tries to recover from the old position, server returns
	// error 112, SDK resets position and calls GetState again.
	_ = client.Connect()
	waitSubscribed(t, subscribedCh)
	if calls := atomic.LoadInt64(&getStateCalls); calls != 2 {
		t.Fatalf("expected GetState to be called again after unrecoverable position, got %d calls", calls)
	}

	// Verify live delivery works after the GetState re-sync.
	if _, err := publisher.Publish(context.Background(), channel, []byte(`{"live":true}`)); err != nil {
		t.Fatalf("publish error: %v", err)
	}
	select {
	case <-pubCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for live publication after re-sync")
	}
}
