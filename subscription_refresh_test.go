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

// Regression test for sub refresh failures being reported as
// SubscriptionSubscribeError: a failed sub_refresh command must surface as
// SubscriptionRefreshError so apps can tell "my subscription token could not be
// renewed" apart from "subscribing failed". The GetToken failure path in the
// same flow already emitted SubscriptionRefreshError, and the connection-level
// counterpart (Client.sendRefresh) consistently emits RefreshError.
func TestSubRefreshErrorEmitsRefreshError(t *testing.T) {
	server := NewFakeServer(t)
	server.OnSubscribe = func(_ string, _ *protocol.SubscribeRequest) *protocol.SubscribeResult {
		return &protocol.SubscribeResult{Expires: true, Ttl: 1}
	}
	// Fail the sub_refresh command with a temporary server error: the
	// subscription stays subscribed and the SDK retries, but the app must be
	// told about the refresh failure.
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.SubRefresh == nil {
			return nil
		}
		return &protocol.Reply{Id: cmd.Id, Error: &protocol.Error{
			Code: 108, Message: "not available", Temporary: true,
		}}
	}

	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetToken: func(_ SubscriptionTokenEvent) (string, error) {
			return "token", nil
		},
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}

	subscribedCh := make(chan SubscribedEvent, 4)
	errCh := make(chan SubscriptionErrorEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	sub.OnError(func(e SubscriptionErrorEvent) { errCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	ev := waitCh(t, errCh, "refresh error")
	var refreshErr SubscriptionRefreshError
	if !errors.As(ev.Error, &refreshErr) {
		t.Fatalf("expected SubscriptionRefreshError, got %T: %v", ev.Error, ev.Error)
	}
	var serverErr *Error
	if !errors.As(ev.Error, &serverErr) || serverErr.Code != 108 {
		t.Fatalf("expected wrapped server error with code 108, got %v", ev.Error)
	}
	if state := sub.State(); state != SubStateSubscribed {
		t.Fatalf("expected subscription to stay subscribed after temporary refresh error, got %s", state)
	}
}
