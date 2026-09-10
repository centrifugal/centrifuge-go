package centrifuge

import (
	"errors"
	"testing"

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
