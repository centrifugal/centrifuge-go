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
	"fmt"
	"sync/atomic"
	"testing"
	"time"

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

// Regression test for the refreshed subscription token not being cached: the
// token obtained during a sub refresh must replace the previously cached one,
// so a later resubscribe (after a reconnect) sends the fresh token. Before the
// fix the subscription kept the token it first subscribed with, and every
// resubscribe after a refresh sent an expired token — the server rejected it
// with error 109, the SDK emitted a spurious SubscriptionSubscribeError and
// only then retried with a new token. centrifuge-js caches it (subscription.ts
// _refresh), as does Client.sendRefresh here for the connection token.
func TestSubRefreshCachesRefreshedToken(t *testing.T) {
	server := NewFakeServer(t)
	server.OnSubscribe = func(_ string, _ *protocol.SubscribeRequest) *protocol.SubscribeResult {
		// Expiring subscription: the SDK schedules a refresh in 1 second.
		return &protocol.SubscribeResult{Expires: true, Ttl: 1}
	}
	refreshedCh := make(chan string, 4)
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.SubRefresh == nil {
			return nil
		}
		select {
		case refreshedCh <- cmd.SubRefresh.Token:
		default:
		}
		// Non-expiring result — no further refresh is scheduled.
		return &protocol.Reply{Id: cmd.Id, SubRefresh: &protocol.SubRefreshResult{}}
	}

	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	var tokenCalls int32
	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetToken: func(_ SubscriptionTokenEvent) (string, error) {
			return fmt.Sprintf("token-%d", atomic.AddInt32(&tokenCalls, 1)), nil
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

	refreshedToken := waitCh(t, refreshedCh, "sub refresh")
	if refreshedToken != "token-2" {
		t.Fatalf("expected refresh with token-2, got %q", refreshedToken)
	}

	// Drop the connection: the client reconnects and resubscribes, and must
	// use the refreshed token rather than the one it initially subscribed with.
	server.CloseConnection()
	waitCh(t, subscribedCh, "resubscribed")

	if token := server.LastSubscribe().Token; token != refreshedToken {
		t.Fatalf("resubscribe must use the refreshed token %q, got %q", refreshedToken, token)
	}
	select {
	case ev := <-errCh:
		t.Fatalf("unexpected subscription error: %v", ev.Error)
	default:
	}
}

// A subscription refresh whose GetToken returns after a reconnect belongs to the
// earlier subscribed session. Sent while the subscription resubscribes, the
// server rejects it as not subscribed (103), which unsubscribes for good; sent
// after the resubscribe, it doubles the refresh chain.
func TestSubRefreshStartedBeforeResubscribeStops(t *testing.T) {
	server := NewFakeServer(t)
	server.OnSubscribe = func(_ string, _ *protocol.SubscribeRequest) *protocol.SubscribeResult {
		return &protocol.SubscribeResult{Expires: true, Ttl: 1}
	}
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.SubRefresh == nil {
			return nil
		}
		return &protocol.Reply{Id: cmd.Id, SubRefresh: &protocol.SubRefreshResult{}}
	}
	client := NewProtobufClient(server.URL(), Config{
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	t.Cleanup(client.Close)

	refreshTokenRequested := make(chan struct{})
	releaseRefreshToken := make(chan struct{})
	var tokenCalls atomic.Int32
	sub, err := client.NewSubscription("ch", SubscriptionConfig{
		GetToken: func(_ SubscriptionTokenEvent) (string, error) {
			switch tokenCalls.Add(1) {
			case 1:
				return "subscribe-token", nil
			case 2:
				close(refreshTokenRequested)
				<-releaseRefreshToken
				return "stale-refresh-token", nil
			default:
				return "refresh-token", nil
			}
		},
	})
	if err != nil {
		t.Fatalf("new subscription: %v", err)
	}
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")
	waitCh(t, refreshTokenRequested, "refresh GetToken call")
	server.CloseConnection()
	waitCh(t, subscribedCh, "resubscribed")
	receivedBefore := len(server.Received())
	close(releaseRefreshToken)
	// Less than the new subscribed session's own refresh delay (1s).
	time.Sleep(300 * time.Millisecond)

	for _, cmd := range server.Received()[receivedBefore:] {
		if cmd.SubRefresh != nil {
			t.Fatalf("sub refresh started before the reconnect was sent with token %q", cmd.SubRefresh.Token)
		}
	}
	if state := sub.State(); state != SubStateSubscribed {
		t.Fatalf("expected subscribed, got %s", state)
	}
}

// A failed sub refresh is reported to OnError, which waits for the handler,
// before the subscription unsubscribes. If the subscription resubscribed in the
// meantime (for example after a reconnect), the failure belongs to the ended
// subscribed session and must not unsubscribe the new one.
func TestSubRefreshErrorOfEndedSessionDoesNotUnsubscribe(t *testing.T) {
	server := NewFakeServer(t)
	var subscribes atomic.Int32
	server.OnSubscribe = func(_ string, _ *protocol.SubscribeRequest) *protocol.SubscribeResult {
		if subscribes.Add(1) == 1 {
			return &protocol.SubscribeResult{Expires: true, Ttl: 1}
		}
		return &protocol.SubscribeResult{}
	}
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.SubRefresh == nil {
			return nil
		}
		return &protocol.Reply{Id: cmd.Id, Error: &protocol.Error{Code: 103, Message: "permission denied"}}
	}
	client := NewProtobufClient(server.URL(), Config{
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
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
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	refreshErrorStarted := make(chan struct{})
	releaseRefreshError := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseRefreshError:
		default:
			close(releaseRefreshError)
		}
	})
	var errorCalls atomic.Int32
	sub.OnError(func(_ SubscriptionErrorEvent) {
		if errorCalls.Add(1) == 1 {
			close(refreshErrorStarted)
			<-releaseRefreshError
		}
	})

	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")
	waitCh(t, refreshErrorStarted, "refresh error")
	// The refresh reply is handled on the reader of the connection, which the
	// handler blocks, so the app starts a new connection.
	_ = client.Disconnect()
	_ = client.Connect()
	// OnSubscribed can't run while the OnError handler blocks, so poll.
	deadline := time.Now().Add(5 * time.Second)
	for {
		sub.mu.RLock()
		resubscribed := sub.state == SubStateSubscribed && sub.subscribedSession == 2
		sub.mu.RUnlock()
		if resubscribed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout waiting for resubscribe")
		}
		time.Sleep(10 * time.Millisecond)
	}
	receivedBefore := len(server.Received())
	close(releaseRefreshError)
	time.Sleep(300 * time.Millisecond)

	for _, cmd := range server.Received()[receivedBefore:] {
		if cmd.Unsubscribe != nil {
			t.Fatal("refresh error of the ended subscribed session sent unsubscribe")
		}
	}
	if state := sub.State(); state != SubStateSubscribed {
		t.Fatalf("expected subscribed, got %s", state)
	}
}
