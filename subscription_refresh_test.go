package centrifuge

// Regression test for a deadlock in the subscription refresh error path: on a
// GetToken failure during sub refresh, emitError (which synchronously waits for
// the OnError handler to run on the client's callback-dispatch goroutine) was
// called while holding s.mu. If the OnError handler touched the Subscription's
// own lock (e.g. calling State()), the dispatch goroutine would block on s.mu
// forever while the goroutine holding s.mu waited on the dispatch goroutine —
// a classic deadlock that would also freeze the shared callback queue for the
// whole client. See subscription.go scheduleSubRefresh.

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/centrifugal/protocol"
)

func TestSubRefreshErrorHandlerDoesNotDeadlock(t *testing.T) {
	server := NewFakeServer(t)
	server.OnSubscribe = func(_ string, _ *protocol.SubscribeRequest) *protocol.SubscribeResult {
		return &protocol.SubscribeResult{Expires: true, Ttl: 1}
	}

	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	var tokenCalls int32
	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetToken: func(_ SubscriptionTokenEvent) (string, error) {
			if atomic.AddInt32(&tokenCalls, 1) == 1 {
				return "initial-token", nil
			}
			return "", errors.New("boom")
		},
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}

	subscribedCh := make(chan SubscribedEvent, 4)
	errCh := make(chan SubscriptionErrorEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	sub.OnError(func(e SubscriptionErrorEvent) {
		// Touching the Subscription's own lock from within the handler must
		// not deadlock even if the handler ran while emitError's caller held
		// s.mu.
		_ = sub.State()
		errCh <- e
	})

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	// Ttl=1s triggers a refresh, which fails GetToken (second call) and must
	// reach the OnError handler without hanging.
	waitCh(t, errCh, "refresh error")
}
