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

	"github.com/centrifugal/protocol"
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

func TestNoCommandBeforeConnectOnNewTransport(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{})
	closeOnCleanup(t, client)
	connected := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) {
		select {
		case connected <- struct{}{}:
		default:
		}
	})

	// Registering the connect request waits while the test holds requestsMu:
	// the client stops right before writing the connect frame.
	client.requestsMu.Lock()
	go func() { _ = client.Connect() }()
	waitCondition(t, "the client to dial", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.current != nil
	})
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		client.transportMu.RLock()
		visible := client.transport != nil
		client.transportMu.RUnlock()
		if visible {
			break
		}
	}
	// A command sent meanwhile without waiting for the connected state, as a
	// subscription's resubscribe timer or GetToken goroutine does, must not use
	// the new transport yet: a server closes a connection whose first frame
	// isn't connect (3501).
	_ = client.send(&protocol.Command{Id: client.nextCmdID(), Subscribe: &protocol.SubscribeRequest{Channel: "news"}})
	client.requestsMu.Unlock()

	waitCh(t, connected, "connected")
	received := s.Received()
	if len(received) == 0 {
		t.Fatal("no commands received")
	}
	if received[0].Connect == nil {
		t.Fatalf("the first command on the connection must be connect, got %v", received[0])
	}
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

// Connect attempts superseded by Disconnect and a new Connect, and transports
// replaced by a newer connection. Callbacks of the old attempt or transport
// must not act on the client's current connection.

func countConnects(s *FakeServer) int {
	connects := 0
	for _, cmd := range s.Received() {
		if cmd.Connect != nil {
			connects++
		}
	}
	return connects
}

func TestSupersededConnectAttemptKeepsNewToken(t *testing.T) {
	s := NewFakeServer(t)
	firstTokenRequested := make(chan struct{})
	releaseFirstToken := make(chan struct{})
	var tokenCalls atomic.Int32
	client := NewProtobufClient(s.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) {
			if tokenCalls.Add(1) == 1 {
				close(firstTokenRequested)
				<-releaseFirstToken
				return "A", nil
			}
			return "unexpected", nil
		},
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	closeOnCleanup(t, client)
	connectedCh := make(chan struct{}, 4)
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })

	go func() { _ = client.Connect() }()
	waitCh(t, firstTokenRequested, "first GetToken call")
	// Switch the user while the first attempt waits for its token.
	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
	client.SetToken("B")
	_ = client.Connect()
	waitCh(t, connectedCh, "connected with token B")
	close(releaseFirstToken)
	time.Sleep(100 * time.Millisecond)

	s.CloseConnection()
	waitCh(t, connectedCh, "reconnected")
	for _, cmd := range s.Received() {
		if cmd.Connect != nil && cmd.Connect.Token != "B" {
			t.Fatalf("connect sent with token %q after SetToken(\"B\")", cmd.Connect.Token)
		}
	}
}

func TestReplacedTransportCloseDoesNotTearDownNewConnection(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	closeOnCleanup(t, client)
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	subscribedCh := make(chan struct{}, 4)
	sub.OnSubscribed(func(SubscribedEvent) { subscribedCh <- struct{}{} })
	inHandler := make(chan struct{}, 1)
	releaseHandler := make(chan struct{})
	var once sync.Once
	sub.OnPublication(func(PublicationEvent) {
		once.Do(func() {
			inHandler <- struct{}{}
			<-releaseHandler
		})
	})
	_ = sub.Subscribe()
	_ = client.Connect()
	waitCh(t, subscribedCh, "subscribed")

	// The first connection's reader waits in the publication handler while the
	// application reconnects.
	s.PublishChannel("news", []byte(`{}`))
	waitCh(t, inHandler, "publication handler")
	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
	_ = client.Connect()
	waitCondition(t, "new connection", func() bool { return client.State() == StateConnected })
	close(releaseHandler)
	time.Sleep(300 * time.Millisecond)

	if state := client.State(); state != StateConnected {
		t.Fatalf("expected connected, got %s", state)
	}
	if connects := countConnects(s); connects != 2 {
		t.Fatalf("expected 2 connect commands, got %d: the first connection's close tore down the second", connects)
	}
}

func TestTransportClosedWhileConnectingRetriesWithoutConnectTimeout(t *testing.T) {
	s := NewFakeServer(t)
	var connects atomic.Int32
	s.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Connect != nil && connects.Add(1) == 1 {
			// Drop the first connection before its connect reply.
			s.CloseConnection()
		}
		return nil
	}
	client := NewProtobufClient(s.URL(), Config{
		ReadTimeout:       5 * time.Second,
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	closeOnCleanup(t, client)
	connectedCh := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })

	started := time.Now()
	_ = client.Connect()
	select {
	case <-connectedCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("not connected %s after the connection closed while connecting: the client waits for the connect reply timeout (%s)", time.Since(started), client.config.ReadTimeout)
	}
}
