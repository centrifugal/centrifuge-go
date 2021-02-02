package centrifuge

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
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
	onUnsubscribe      func(*Subscription, UnsubscribeEvent)
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

func (h *testSubscriptionHandler) OnUnsubscribe(c *Subscription, e UnsubscribeEvent) {
	if h.onUnsubscribe != nil {
		h.onUnsubscribe(c, e)
	}
}

func TestConnectWrongAddress(t *testing.T) {
	client := New("ws://localhost:9000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
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
	_ = client.Connect()
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
	defer func() { _ = client.Close() }()
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
	_ = client.Connect()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting successful connect")
	}
}

func TestDisconnect(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket?format=protobuf", DefaultConfig())
	defer func() { _ = client.Close() }()
	connectDoneCh := make(chan error, 1)
	disconnectDoneCh := make(chan error, 1)
	handler := &testEventHandler{
		onConnect: func(c *Client, e ConnectEvent) {
			close(connectDoneCh)
		},
		onDisconnect: func(c *Client, e DisconnectEvent) {
			if e.Reconnect != false {
				disconnectDoneCh <- fmt.Errorf("wrong reconnect value")
				return
			}
			close(disconnectDoneCh)
		},
	}
	client.OnConnect(handler)
	client.OnDisconnect(handler)
	_ = client.Connect()
	select {
	case err := <-connectDoneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting successful connect")
	}
	_ = client.Disconnect()
	select {
	case err := <-disconnectDoneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting successful disconnect")
	}
}

func TestPublishProtobuf(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket?format=protobuf", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	_, err := client.Publish("test", []byte("boom"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishJSON(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	_, err := client.Publish("test", []byte("{}"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishInvalidJSON(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	_, err := client.Publish("test", []byte("boom"))
	if err == nil {
		t.Errorf("error expected on publish invalid JSON")
	}
}

func TestSubscribeSuccess(t *testing.T) {
	doneCh := make(chan error, 1)
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	sub, err := client.NewSubscription("test")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	sub.OnSubscribeSuccess(&testSubscriptionHandler{
		onSubscribeSuccess: func(c *Subscription, e SubscribeSuccessEvent) {
			close(doneCh)
		},
	})
	_ = sub.Subscribe()
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
	defer func() { _ = client.Close() }()
	_ = client.Connect()
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
	_ = sub.Subscribe()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting subscribe error")
	}
}

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func randString(n int) string {
	random := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[random.Intn(len(letterRunes))]
	}
	return string(b)
}

func TestHandlePublish(t *testing.T) {
	doneCh := make(chan error, 1)
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	sub, err := client.NewSubscription("test_handle_publish")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
	handler := &testSubscriptionHandler{
		onSubscribeSuccess: func(c *Subscription, e SubscribeSuccessEvent) {
			_, err := client.Publish("test_handle_publish", msg)
			if err != nil {
				t.Fail()
			}
		},
		onPublish: func(c *Subscription, e PublishEvent) {
			if !bytes.Equal(e.Data, msg) {
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
	_ = sub.Subscribe()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting publication received over subscription")
	}
}

func TestSubscriptionClose(t *testing.T) {
	subscribedCh := make(chan struct{}, 1)
	unsubscribedCh := make(chan struct{}, 1)
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	sub, err := client.NewSubscription("test_subscription_close")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	handler := &testSubscriptionHandler{
		onSubscribeSuccess: func(c *Subscription, e SubscribeSuccessEvent) {
			close(subscribedCh)
		},
		onUnsubscribe: func(subscription *Subscription, event UnsubscribeEvent) {
			close(unsubscribedCh)
		},
	}
	sub.OnUnsubscribe(handler)
	sub.OnSubscribeSuccess(handler)
	sub.OnPublish(handler)
	_ = sub.Subscribe()
	select {
	case <-subscribedCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("timeout waiting for subscribe")
	}
	err = sub.Close()
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	select {
	case <-unsubscribedCh:
	case <-time.After(3 * time.Second):
		t.Errorf("timeout waiting for subscribe")
	}
	err = sub.Subscribe()
	if err != ErrSubscriptionClosed {
		t.Fatal("ErrSubscriptionClosed expected on Subscribe after Close")
	}
	err = sub.Close()
	if err != ErrSubscriptionClosed {
		t.Fatal("ErrSubscriptionClosed expected on second Close")
	}
}

func TestClient_Publish(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
	_, err := client.Publish("test", msg)
	if err != nil {
		// Publish should be allowed since we are using Centrifugo in insecure mode in tests.
		t.Fatal(err)
	}
}

func TestClient_Presence(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	_, err := client.Presence("test")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal("expected protocol error")
	}
	if e.Code != 108 {
		t.Fatal("expected not available error, got " + strconv.FormatUint(uint64(e.Code), 10))
	}
}

func TestClient_PresenceStats(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	_, err := client.PresenceStats("test")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal("expected protocol error")
	}
	if e.Code != 108 {
		t.Fatal("expected not available error, got " + strconv.FormatUint(uint64(e.Code), 10))
	}
}

func TestClient_History(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket", DefaultConfig())
	defer func() { _ = client.Close() }()
	_ = client.Connect()
	_, err := client.History("test")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal("expected protocol error")
	}
	if e.Code != 108 {
		t.Fatal("expected not available error, got " + strconv.FormatUint(uint64(e.Code), 10))
	}
}
