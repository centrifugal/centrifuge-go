package centrifuge

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
)

// Server-side subscriptions recover through the connect reply. The stored
// position must only move as recovered publications are delivered, and nothing
// of a connection a handler tore down is emitted afterwards: the publications
// not delivered are recovered by the next connect.
func TestServerSubRecoveredPublicationsNotLostOnTeardown(t *testing.T) {
	for _, tc := range []struct {
		name          string
		wantDelivered int32
		wantOffset    uint64
	}{
		{name: "all delivered", wantDelivered: 3, wantOffset: 8},
		{name: "disconnect in connected", wantDelivered: 0, wantOffset: 5},
		{name: "disconnect in subscribed", wantDelivered: 0, wantOffset: 5},
		{name: "disconnect in first publication", wantDelivered: 1, wantOffset: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewFakeServer(t)
			var connects atomic.Int32
			server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
				if cmd.Connect == nil {
					return nil
				}
				// Like Centrifugo, a successful recovery's reply carries the requested position.
				news := &protocol.SubscribeResult{Recoverable: true, Epoch: "e1", WasRecovering: true, Recovered: true}
				if req := cmd.Connect.Subs["news"]; req != nil {
					news.Offset = req.Offset
				}
				switch connects.Add(1) {
				case 1:
					news = &protocol.SubscribeResult{Recoverable: true, Epoch: "e1", Offset: 5}
				case 2:
					news.Publications = []*protocol.Publication{
						{Offset: 6, Data: []byte(`{}`)}, {Offset: 7, Data: []byte(`{}`)}, {Offset: 8, Data: []byte(`{}`)},
					}
				}
				return &protocol.Reply{Id: cmd.Id, Connect: &protocol.ConnectResult{
					Client: "fake-client", Subs: map[string]*protocol.SubscribeResult{"news": news},
				}}
			}
			client := NewProtobufClient(server.URL(), Config{
				MinReconnectDelay: 10 * time.Millisecond,
				MaxReconnectDelay: 20 * time.Millisecond,
			})
			t.Cleanup(client.Close)

			var disconnectCalled atomic.Bool
			disconnect := func() {
				disconnectCalled.Store(true)
				_ = client.Disconnect()
			}
			connectedCh := make(chan struct{}, 4)
			var connectedCount, subscribedCount, delivered, deliveredAfterDisconnect atomic.Int32
			client.OnConnected(func(ConnectedEvent) {
				if connectedCount.Add(1) == 2 && tc.name == "disconnect in connected" {
					disconnect()
				}
				connectedCh <- struct{}{}
			})
			client.OnSubscribed(func(ServerSubscribedEvent) {
				if subscribedCount.Add(1) == 2 && tc.name == "disconnect in subscribed" {
					disconnect()
				}
			})
			client.OnPublication(func(ServerPublicationEvent) {
				if disconnectCalled.Load() {
					deliveredAfterDisconnect.Add(1)
				}
				if delivered.Add(1) == 1 && tc.name == "disconnect in first publication" {
					disconnect()
				}
			})
			_ = client.Connect()
			waitCh(t, connectedCh, "connected")

			// Reconnect: the connect reply recovers publications 6, 7 and 8.
			server.CloseConnection()
			waitCh(t, connectedCh, "reconnected with recovered publications")
			time.Sleep(150 * time.Millisecond)
			if got := deliveredAfterDisconnect.Load(); got > 0 {
				t.Fatalf("%d recovered publications delivered after Disconnect() from a handler", got)
			}
			if got := delivered.Load(); got != tc.wantDelivered {
				t.Fatalf("expected %d recovered publications delivered, got %d", tc.wantDelivered, got)
			}

			if tc.name == "all delivered" {
				server.CloseConnection()
			} else {
				_ = client.Connect()
			}
			waitCondition(t, "next connect", func() bool { return connects.Load() >= 3 })
			var next *protocol.SubscribeRequest
			for _, cmd := range server.Received() {
				if cmd.Connect != nil {
					next = cmd.Connect.Subs["news"]
				}
			}
			if next == nil || !next.Recover || next.Offset != tc.wantOffset {
				t.Fatalf("next connect recovers news with %+v, expected offset %d: recovered publications not delivered would be lost", next, tc.wantOffset)
			}
		})
	}
}

// A handler disconnecting while one channel's recovered publications are
// delivered must not lose the recovered publications of the other channels.
func TestServerSubRecoveredPublicationsOfOtherChannelsNotLost(t *testing.T) {
	server := NewFakeServer(t)
	var connects atomic.Int32
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Connect == nil {
			return nil
		}
		subs := map[string]*protocol.SubscribeResult{}
		for _, ch := range []string{"a", "b"} {
			// Like Centrifugo, a successful recovery's reply carries the requested position.
			sub := &protocol.SubscribeResult{Recoverable: true, Epoch: "e1", WasRecovering: true, Recovered: true}
			if req := cmd.Connect.Subs[ch]; req != nil {
				sub.Offset = req.Offset
			}
			switch connects.Load() + 1 {
			case 1:
				sub = &protocol.SubscribeResult{Recoverable: true, Epoch: "e1", Offset: 5}
			case 2:
				sub.Publications = []*protocol.Publication{
					{Offset: 6, Data: []byte(`{}`)}, {Offset: 7, Data: []byte(`{}`)}, {Offset: 8, Data: []byte(`{}`)},
				}
			}
			subs[ch] = sub
		}
		connects.Add(1)
		return &protocol.Reply{Id: cmd.Id, Connect: &protocol.ConnectResult{Client: "fake-client", Subs: subs}}
	}
	client := NewProtobufClient(server.URL(), Config{
		MinReconnectDelay: 10 * time.Millisecond,
		MaxReconnectDelay: 20 * time.Millisecond,
	})
	t.Cleanup(client.Close)

	connectedCh := make(chan struct{}, 4)
	var connectedCount atomic.Int32
	client.OnConnected(func(ConnectedEvent) {
		connectedCount.Add(1)
		connectedCh <- struct{}{}
	})
	var delivered atomic.Int32
	var deliveredChannel atomic.Value
	client.OnPublication(func(e ServerPublicationEvent) {
		if connectedCount.Load() == 2 && delivered.Add(1) == 1 {
			deliveredChannel.Store(e.Channel)
			_ = client.Disconnect()
		}
	})
	_ = client.Connect()
	waitCh(t, connectedCh, "connected")

	// Reconnect: the connect reply recovers publications 6, 7 and 8 of both channels.
	server.CloseConnection()
	waitCh(t, connectedCh, "reconnected with recovered publications")
	time.Sleep(150 * time.Millisecond)
	if got := delivered.Load(); got != 1 {
		t.Fatalf("expected 1 recovered publication delivered before Disconnect(), got %d", got)
	}

	_ = client.Connect()
	waitCondition(t, "next connect", func() bool { return connects.Load() >= 3 })
	var next map[string]*protocol.SubscribeRequest
	for _, cmd := range server.Received() {
		if cmd.Connect != nil {
			next = cmd.Connect.Subs
		}
	}
	first, _ := deliveredChannel.Load().(string)
	for _, ch := range []string{"a", "b"} {
		want := uint64(5)
		if ch == first {
			want = 6
		}
		if sub := next[ch]; sub == nil || !sub.Recover || sub.Offset != want {
			t.Fatalf("next connect recovers %s with %+v, expected offset %d", ch, sub, want)
		}
	}
}

func TestServerSubMissingFromConnectReplyIsRemoved(t *testing.T) {
	server := NewFakeServer(t)
	var numConnects atomic.Int32
	server.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Connect == nil {
			return nil
		}
		res := &protocol.ConnectResult{Client: "fake-client"}
		if numConnects.Add(1) == 1 {
			// Only the first connection has the server-side subscription.
			res.Subs = map[string]*protocol.SubscribeResult{
				"news": {Recoverable: true, Epoch: "e1", Offset: 5},
			}
		}
		return &protocol.Reply{Id: cmd.Id, Connect: res}
	}
	client := NewProtobufClient(server.URL(), Config{})
	t.Cleanup(client.Close)

	subscribedCh := make(chan ServerSubscribedEvent, 4)
	unsubscribedCh := make(chan ServerUnsubscribedEvent, 4)
	connectedCh := make(chan ConnectedEvent, 4)
	client.OnSubscribed(func(e ServerSubscribedEvent) { subscribedCh <- e })
	client.OnUnsubscribed(func(e ServerUnsubscribedEvent) { unsubscribedCh <- e })
	client.OnConnected(func(e ConnectedEvent) { connectedCh <- e })

	_ = client.Connect()
	waitCh(t, connectedCh, "connected")
	waitCh(t, subscribedCh, "server-side subscribed")

	// Reconnect: the server no longer has the subscription.
	server.CloseConnection()
	waitCh(t, connectedCh, "reconnected")
	if ev := waitCh(t, unsubscribedCh, "server-side unsubscribed"); ev.Channel != "news" {
		t.Fatalf("unexpected unsubscribed channel: %q", ev.Channel)
	}

	client.mu.RLock()
	_, stale := client.serverSubs["news"]
	client.mu.RUnlock()
	if stale {
		t.Fatal("server-side sub missing from connect reply must be removed")
	}

	// A later server-side subscribe to the same channel must be reported.
	server.SendPush(&protocol.Push{Channel: "news", Subscribe: &protocol.Subscribe{}})
	if ev := waitCh(t, subscribedCh, "server-side subscribed again"); ev.Channel != "news" {
		t.Fatalf("unexpected subscribed channel: %q", ev.Channel)
	}
}
