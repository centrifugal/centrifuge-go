package centrifuge

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// connCountingServer accepts WebSocket connections and counts how many were
// opened and how many were then closed by the client. It never replies, which
// is enough here: the client dials the transport before it calls GetToken, so
// these tests never get as far as sending a connect command.
type connCountingServer struct {
	srv    *httptest.Server
	opened atomic.Int64
	closed atomic.Int64

	mu    sync.Mutex
	conns []*websocket.Conn
}

func newConnCountingServer(t *testing.T) *connCountingServer {
	t.Helper()
	s := &connCountingServer{}
	upgrader := websocket.Upgrader{Subprotocols: []string{"centrifuge-protobuf"}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.opened.Add(1)
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				s.closed.Add(1)
				return
			}
		}
	}))
	t.Cleanup(func() {
		s.mu.Lock()
		for _, conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.srv.Close()
	})
	return s
}

func (s *connCountingServer) URL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/connection/websocket"
}

func waitCondition(t *testing.T, label string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertAllConnsClosed waits until every connection the client opened has been
// closed by the client.
func (s *connCountingServer) assertAllConnsClosed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		opened, closed := s.opened.Load(), s.closed.Load()
		if opened > 0 && opened == closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("client leaked transports: opened %d connections, closed %d", opened, closed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Regression tests for the token refresh step of Client.startReconnecting.
// The transport is dialed before GetToken is called, so every path that
// abandons the attempt after the token step must close it. Otherwise the
// WebSocket connection and its reader goroutine leak until the server drops
// the unauthenticated connection.

func TestReconnectClosesTransportOnGetTokenError(t *testing.T) {
	server := newConnCountingServer(t)
	client := NewProtobufClient(server.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) {
			return "", errors.New("token service unavailable")
		},
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	_ = client.Connect()
	// Several attempts, each dialing a transport and failing to get a token.
	waitCondition(t, "several reconnect attempts", func() bool { return server.opened.Load() >= 3 })
	client.Close()
	server.assertAllConnsClosed(t)
}

func TestReconnectClosesTransportOnGetTokenUnauthorized(t *testing.T) {
	server := newConnCountingServer(t)
	client := NewProtobufClient(server.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) {
			return "", ErrUnauthorized
		},
	})
	t.Cleanup(client.Close)
	_ = client.Connect()
	if state := client.State(); state != StateDisconnected {
		t.Fatalf("expected disconnected state after unauthorized, got %s", state)
	}
	server.assertAllConnsClosed(t)
}

func TestReconnectClosesTransportWhenDisconnectedDuringGetToken(t *testing.T) {
	server := newConnCountingServer(t)
	getTokenCalled := make(chan struct{}, 1)
	releaseToken := make(chan struct{})
	client := NewProtobufClient(server.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) {
			getTokenCalled <- struct{}{}
			<-releaseToken
			return "token", nil
		},
	})
	t.Cleanup(client.Close)

	connectDone := make(chan error, 1)
	go func() { connectDone <- client.Connect() }()

	waitCh(t, getTokenCalled, "GetToken call")
	if err := client.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	close(releaseToken)
	_ = waitCh(t, connectDone, "Connect to return")

	server.assertAllConnsClosed(t)
}

// Subscription tokens fetched when subscriptions resubscribe on connect. That
// resubscribe runs while the connect reply callback holds the client mutex, so
// GetToken must not run there: a failure is reported through event handlers,
// which take the mutex, and a slow GetToken or one calling client methods would
// block the whole client.

// stateReturnsWithin reports whether client.State() returns within d.
func stateReturnsWithin(client *Client, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		client.State()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// closeOnCleanup closes the client when the test ends without waiting more
// than a second, so a client deadlocked by the bug under test fails the test's
// assertion instead of hanging it.
func closeOnCleanup(t *testing.T, client *Client) {
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() {
			client.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second):
		}
	})
}

// subscribeBeforeConnect registers the subscriptions before connecting, so
// their tokens are fetched by the resubscribe on connect, and waits until the
// client is connected.
func subscribeBeforeConnect(t *testing.T, client *Client, subs ...*Subscription) {
	t.Helper()
	connectedCh := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })
	for _, sub := range subs {
		_ = sub.Subscribe()
	}
	_ = client.Connect()
	waitCh(t, connectedCh, "connected")
	// Let the connect reply callback reach the resubscribe.
	time.Sleep(50 * time.Millisecond)
}

func TestSubscriptionGetTokenFailureOnConnectDoesNotBlockClient(t *testing.T) {
	for _, tc := range []struct {
		name         string
		err          error
		unsubscribed bool
	}{
		{name: "error", err: errors.New("token service unavailable")},
		{name: "unauthorized", err: ErrUnauthorized, unsubscribed: true},
		{name: "empty token", unsubscribed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewFakeServer(t)
			client := NewProtobufClient(s.URL(), Config{})
			closeOnCleanup(t, client)
			sub, err := client.NewSubscription("private", SubscriptionConfig{
				GetToken: func(SubscriptionTokenEvent) (string, error) {
					return "", tc.err
				},
				MinResubscribeDelay: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			errCh := make(chan SubscriptionErrorEvent, 16)
			sub.OnError(func(e SubscriptionErrorEvent) {
				select {
				case errCh <- e:
				default:
				}
			})
			unsubscribedCh := make(chan UnsubscribedEvent, 1)
			sub.OnUnsubscribed(func(e UnsubscribedEvent) { unsubscribedCh <- e })

			subscribeBeforeConnect(t, client, sub)
			if !stateReturnsWithin(client, time.Second) {
				t.Fatal("client.State() blocked after a subscription token failure on connect")
			}
			if tc.unsubscribed {
				if e := waitCh(t, unsubscribedCh, "unsubscribed"); e.Code != unsubscribedUnauthorized {
					t.Fatalf("expected unauthorized unsubscribe, got %d", e.Code)
				}
			} else {
				waitCh(t, errCh, "subscription error")
			}
			disconnected := make(chan error, 1)
			go func() { disconnected <- client.Disconnect() }()
			if err := waitCh(t, disconnected, "Disconnect to return"); err != nil {
				t.Fatalf("disconnect: %v", err)
			}
		})
	}
}

func TestSubscriptionGetTokenCanCallClientOnConnect(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{})
	closeOnCleanup(t, client)
	sub, err := client.NewSubscription("private", SubscriptionConfig{
		GetToken: func(SubscriptionTokenEvent) (string, error) {
			return fmt.Sprintf("token-%s", client.State()), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	subscribedCh := make(chan SubscribedEvent, 1)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })

	subscribeBeforeConnect(t, client, sub)
	if !stateReturnsWithin(client, time.Second) {
		t.Fatal("client blocked by a subscription GetToken calling client.State() on connect")
	}
	waitCh(t, subscribedCh, "subscribed")
	if token := s.LastSubscribe().Token; token != "token-connected" {
		t.Fatalf("unexpected subscribe token %q", token)
	}
}

func TestSlowSubscriptionTokensOnConnectDoNotBlockClient(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{})
	closeOnCleanup(t, client)
	const numSubs = 5
	subscribedCh := make(chan struct{}, numSubs)
	subs := make([]*Subscription, 0, numSubs)
	for i := 0; i < numSubs; i++ {
		sub, err := client.NewSubscription(fmt.Sprintf("private%d", i), SubscriptionConfig{
			GetToken: func(SubscriptionTokenEvent) (string, error) {
				time.Sleep(300 * time.Millisecond)
				return "token", nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		sub.OnSubscribed(func(SubscribedEvent) { subscribedCh <- struct{}{} })
		subs = append(subs, sub)
	}

	subscribeBeforeConnect(t, client, subs...)
	started := time.Now()
	client.State()
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("client.State() blocked for %s while subscription tokens were fetched", elapsed)
	}
	for i := 0; i < numSubs; i++ {
		waitCh(t, subscribedCh, "subscribed")
	}
}
