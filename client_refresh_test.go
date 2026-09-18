package centrifuge

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

// Regression test for the temporary connection refresh error path in
// Client.sendRefresh. When the server rejected the refresh command with a
// temporary error, the reply callback (running on the reader goroutine) called
// handleError while holding c.mu. handleError dispatches through
// runHandlerSync, which takes c.mu.RLock — so the reader goroutine deadlocked
// on itself, freezing the connection and every Client method needing c.mu.
// The same call also wrapped the (always nil) transport error instead of the
// server error from the reply, so OnError would have received
// "refresh error: <nil>". centrifuge-js emits the server error in this case.
func TestRefreshTemporaryErrorEmitsServerError(t *testing.T) {
	server := NewFakeServer(t)
	// Expiring connection: the SDK schedules a refresh in 1 second.
	server.ConnectResult = &protocol.ConnectResult{
		Client: "fake-client", Version: "0.0.0", Ping: 25, Expires: true, Ttl: 1,
	}
	// Fail the refresh command with a temporary server error: the client stays
	// connected and retries later, but the app must be told why.
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Refresh == nil {
			return nil
		}
		return &protocol.Reply{Id: cmd.Id, Error: &protocol.Error{
			Code: 108, Message: "not available", Temporary: true,
		}}
	}

	client := NewProtobufClient(server.URL(), Config{
		Token: "token",
		GetToken: func(_ ConnectionTokenEvent) (string, error) {
			return "refreshed-token", nil
		},
	})
	t.Cleanup(client.Close)

	connectedCh := make(chan ConnectedEvent, 4)
	errCh := make(chan ErrorEvent, 4)
	client.OnConnected(func(e ConnectedEvent) { connectedCh <- e })
	client.OnError(func(e ErrorEvent) { errCh <- e })

	_ = client.Connect()
	waitCh(t, connectedCh, "connected")

	ev := waitCh(t, errCh, "refresh error")
	var refreshErr RefreshError
	if !errors.As(ev.Error, &refreshErr) {
		t.Fatalf("expected RefreshError, got %T: %v", ev.Error, ev.Error)
	}
	var serverErr *Error
	if !errors.As(ev.Error, &serverErr) || serverErr.Code != 108 {
		t.Fatalf("expected wrapped server error with code 108, got %v", ev.Error)
	}
	if state := client.State(); state != StateConnected {
		t.Fatalf("expected client to stay connected after temporary refresh error, got %s", state)
	}
}

// A connection refresh whose GetToken returns after the connection was replaced
// belongs to the old connection: it must not send a refresh on the new one or
// store its token, which would also start a second refresh chain next to the new
// connection's own.
func TestRefreshStartedBeforeReconnectStops(t *testing.T) {
	server := NewFakeServer(t)
	server.ConnectResult = &protocol.ConnectResult{
		Client: "fake-client", Version: "0.0.0", Ping: 25, Expires: true, Ttl: 1,
	}
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Refresh == nil {
			return nil
		}
		return &protocol.Reply{Id: cmd.Id, Refresh: &protocol.RefreshResult{}}
	}
	refreshTokenRequested := make(chan struct{})
	releaseRefreshToken := make(chan struct{})
	var tokenCalls atomic.Int32
	client := NewProtobufClient(server.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) {
			switch tokenCalls.Add(1) {
			case 1:
				return "connect-token", nil
			case 2:
				close(refreshTokenRequested)
				<-releaseRefreshToken
				return "stale-refresh-token", nil
			default:
				return "refresh-token", nil
			}
		},
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	t.Cleanup(client.Close)
	connectedCh := make(chan ConnectedEvent, 4)
	client.OnConnected(func(e ConnectedEvent) { connectedCh <- e })

	_ = client.Connect()
	waitCh(t, connectedCh, "connected")
	waitCh(t, refreshTokenRequested, "refresh GetToken call")
	server.CloseConnection()
	waitCh(t, connectedCh, "reconnected")
	receivedBefore := len(server.Received())
	close(releaseRefreshToken)
	// Less than the new connection's own refresh delay (1s).
	time.Sleep(300 * time.Millisecond)

	for _, cmd := range server.Received()[receivedBefore:] {
		if cmd.Refresh != nil {
			t.Fatalf("refresh started on the previous connection was sent on the new one with token %q", cmd.Refresh.Token)
		}
	}
	client.mu.RLock()
	token := client.token
	client.mu.RUnlock()
	if token == "stale-refresh-token" {
		t.Fatal("token of a refresh started on the previous connection replaced the client's token")
	}
}

func TestRefreshEmptyTokenDisconnectsAsUnauthorized(t *testing.T) {
	server := NewFakeServer(t)
	server.ConnectResult = &protocol.ConnectResult{
		Client: "fake-client", Version: "0.0.0", Ping: 25, Expires: true, Ttl: 1,
	}
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Refresh == nil {
			return nil
		}
		// Centrifugo closes a connection refreshing with an empty token as a bad request.
		server.DisconnectPush(3501, "bad request")
		return &protocol.Reply{Id: cmd.Id, Refresh: &protocol.RefreshResult{}}
	}
	var tokenCalls atomic.Int32
	client := NewProtobufClient(server.URL(), Config{
		GetToken: func(ConnectionTokenEvent) (string, error) {
			if tokenCalls.Add(1) == 1 {
				return "connect-token", nil
			}
			return "", nil
		},
	})
	t.Cleanup(client.Close)
	connectedCh := make(chan ConnectedEvent, 1)
	client.OnConnected(func(e ConnectedEvent) { connectedCh <- e })
	disconnectedCh := make(chan DisconnectedEvent, 1)
	client.OnDisconnected(func(e DisconnectedEvent) { disconnectedCh <- e })

	_ = client.Connect()
	waitCh(t, connectedCh, "connected")
	if e := waitCh(t, disconnectedCh, "disconnected"); e.Code != disconnectedUnauthorized {
		t.Fatalf("expected an unauthorized disconnect (code %d) for an empty refresh token, got %d %q", disconnectedUnauthorized, e.Code, e.Reason)
	}
}
