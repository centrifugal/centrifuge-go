package centrifuge

import (
	"sync/atomic"
	"testing"

	"github.com/centrifugal/protocol"
)

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
