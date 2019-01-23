package centrifuge

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

type testEventHandler struct {
	onConnect    func(*Client, ConnectEvent)
	onDisconnect func(*Client, DisconnectEvent)
}

func (h *testEventHandler) OnConnect(c *Client, e ConnectEvent) {
	if h.onConnect != nil {
		h.onConnect(c, e)
	}
}

func (h *testEventHandler) OnDisconnect(c *Client, e DisconnectEvent) {
	if h.onDisconnect != nil {
		h.onDisconnect(c, e)
	}
}

type testSubscriptionHandler struct {
	onSubscribeSuccess func(*Subscription, SubscribeSuccessEvent)
	onSubscribeError   func(*Subscription, SubscribeErrorEvent)
	onPublish          func(*Subscription, PublishEvent)
}

func (h *testSubscriptionHandler) OnSubscribeSuccess(c *Subscription, e SubscribeSuccessEvent) {
	if h.onSubscribeSuccess != nil {
		h.onSubscribeSuccess(c, e)
	}
}

func (h *testSubscriptionHandler) OnSubscribeError(c *Subscription, e SubscribeErrorEvent) {
	if h.onSubscribeError != nil {
		h.onSubscribeError(c, e)
	}
}

func (h *testSubscriptionHandler) OnPublish(c *Subscription, e PublishEvent) {
	if h.onPublish != nil {
		h.onPublish(c, e)
	}
}

func TestConnectWrongAddress(t *testing.T) {
	client := New("ws://localhost:9000/connection/websocket", DefaultConfig())
	doneCh := make(chan error, 1)
	handler := &testEventHandler{
		onDisconnect: func(c *Client, e DisconnectEvent) {
			if e.Reconnect != true {
				doneCh <- fmt.Errorf("wrong reconnect value")
				return
			}
			close(doneCh)
		},
	}
	client.OnDisconnect(handler)
	client.Connect()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting disconnect due to malformed address")
	}
}

func TestSuccessfulConnect(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket?format=protobuf", DefaultConfig())
	doneCh := make(chan error, 1)
	handler := &testEventHandler{
		onConnect: func(c *Client, e ConnectEvent) {
			if e.ClientID == "" {
				doneCh <- fmt.Errorf("wrong client ID value")
				return
			}
			close(doneCh)
		},
	}
	client.OnConnect(handler)
	client.Connect()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting successful connect")
	}
}

func TestPublishProtobuf(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket?format=protobuf", DefaultConfig())
	client.Connect()
	err := client.Publish("test", []byte("boom"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishJSON(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket?format=protobuf", DefaultConfig())
	client.Connect()
	err := client.Publish("test", []byte("{}"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishInvalidJSON(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	client.Connect()
	err := client.Publish("test", []byte("boom"))
	if err == nil {
		t.Errorf("error expected on publish invalid JSON")
	}
}

func TestSubscribeSuccess(t *testing.T) {
	doneCh := make(chan error, 1)
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	client.Connect()
	sub, err := client.NewSubscription("test")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	sub.OnSubscribeSuccess(&testSubscriptionHandler{
		onSubscribeSuccess: func(c *Subscription, e SubscribeSuccessEvent) {
			close(doneCh)
		},
	})
	sub.Subscribe()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting successful subscribe")
	}
}

func TestSubscribeError(t *testing.T) {
	doneCh := make(chan error, 1)
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	client.Connect()
	sub, err := client.NewSubscription("test:test")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	sub.OnSubscribeError(&testSubscriptionHandler{
		onSubscribeError: func(c *Subscription, e SubscribeErrorEvent) {
			// Due to unknown namespace.
			close(doneCh)
		},
	})
	sub.Subscribe()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting subscribe error")
	}
}

func TestHandlePublish(t *testing.T) {
	doneCh := make(chan error, 1)
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	client.Connect()
	sub, err := client.NewSubscription("test")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	handler := &testSubscriptionHandler{
		onSubscribeSuccess: func(c *Subscription, e SubscribeSuccessEvent) {
			client.Publish("test", []byte(`{}`))
		},
		onPublish: func(c *Subscription, e PublishEvent) {
			if !bytes.Equal(e.Data, []byte(`{}`)) {
				doneCh <- fmt.Errorf("wrong publication data")
				return
			}
			if e.Info == nil {
				doneCh <- fmt.Errorf("expecting non nil publication info")
				return
			}
			close(doneCh)
		},
	}
	sub.OnSubscribeSuccess(handler)
	sub.OnPublish(handler)
	sub.Subscribe()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting publication received over subscription")
	}
}
