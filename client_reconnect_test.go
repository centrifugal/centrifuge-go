package centrifuge

import (
	"errors"
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
