package centrifuge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

type testEventHandler struct {
	onConnected    func(ConnectedEvent)
	onDisconnected func(DisconnectedEvent)
	onError        func(ErrorEvent)
}

func (h *testEventHandler) OnConnected(e ConnectedEvent) {
	if h.onConnected != nil {
		h.onConnected(e)
	}
}

func (h *testEventHandler) OnDisconnected(e DisconnectedEvent) {
	if h.onDisconnected != nil {
		h.onDisconnected(e)
	}
}

func (h *testEventHandler) OnError(e ErrorEvent) {
	if h.onError != nil {
		h.onError(e)
	}
}

type testSubscriptionHandler struct {
	onSubscribe   func(SubscribedEvent)
	onError       func(SubscriptionErrorEvent)
	onPublication func(PublicationEvent)
	onUnsubscribe func(UnsubscribedEvent)
}

func (h *testSubscriptionHandler) OnSubscribe(e SubscribedEvent) {
	if h.onSubscribe != nil {
		h.onSubscribe(e)
	}
}

func (h *testSubscriptionHandler) OnError(e SubscriptionErrorEvent) {
	if h.onError != nil {
		h.onError(e)
	}
}

func (h *testSubscriptionHandler) OnPublication(e PublicationEvent) {
	if h.onPublication != nil {
		h.onPublication(e)
	}
}

func (h *testSubscriptionHandler) OnUnsubscribe(e UnsubscribedEvent) {
	if h.onUnsubscribe != nil {
		h.onUnsubscribe(e)
	}
}

func TestConnectWrongAddress(t *testing.T) {
	client := NewJsonClient("ws://localhost:9000/connection/websocket", Config{})
	defer client.Close()
	doneCh := make(chan error, 1)
	handler := &testEventHandler{
		onError: func(e ErrorEvent) {
			var err TransportError
			if !errors.As(e.Error, &err) {
				doneCh <- fmt.Errorf("wrong error")
				return
			}
			close(doneCh)
		},
	}
	client.OnError(handler.OnError)
	_ = client.Connect()
	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting moveToDisconnected due to malformed address")
	}
}

func TestSuccessfulConnect(t *testing.T) {
	client := NewProtobufClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	doneCh := make(chan error, 1)
	client.OnConnected(func(e ConnectedEvent) {
		if e.ClientID == "" {
			doneCh <- fmt.Errorf("wrong client ID value")
			return
		}
		close(doneCh)
	})
	client.OnError(func(e ErrorEvent) {
		t.Log(e.Error)
	})
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
	client := NewProtobufClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	connectDoneCh := make(chan error, 1)
	disconnectDoneCh := make(chan error, 1)
	handler := &testEventHandler{
		onConnected: func(e ConnectedEvent) {
			close(connectDoneCh)
		},
		onDisconnected: func(e DisconnectedEvent) {
			close(disconnectDoneCh)
		},
	}
	client.OnConnected(handler.OnConnected)
	client.OnDisconnected(handler.OnDisconnected)
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
		t.Errorf("expecting successful moveToDisconnected")
	}
}

func TestPublishProtobuf(t *testing.T) {
	client := NewProtobufClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	_, err := client.Publish(context.Background(), "test", []byte("boom"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishJSON(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	_, err := client.Publish(context.Background(), "test", []byte("{}"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishInvalidJSON(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	_, err := client.Publish(context.Background(), "test", []byte("boom"))
	if err == nil {
		t.Errorf("error expected on publish invalid JSON")
	}
}

func TestSubscribeSuccess(t *testing.T) {
	doneCh := make(chan error, 1)
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	sub, err := client.NewSubscription("test")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	subHandler := &testSubscriptionHandler{
		onSubscribe: func(e SubscribedEvent) {
			close(doneCh)
		},
	}
	sub.OnSubscribed(subHandler.OnSubscribe)
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
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	sub, err := client.NewSubscription("test:test")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	subHandler := &testSubscriptionHandler{
		onError: func(e SubscriptionErrorEvent) {
			// Due to unknown namespace.
			close(doneCh)
		},
	}
	sub.OnError(subHandler.OnError)
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
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	sub, err := client.NewSubscription("test_handle_publish")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)

	publishOkCh := make(chan struct{})

	sub.OnSubscribed(func(e SubscribedEvent) {
		go func() {
			_, err := client.Publish(context.Background(), "test_handle_publish", msg)
			if err != nil {
				t.Fail()
			}
			close(publishOkCh)
		}()
	})
	sub.OnPublication(func(e PublicationEvent) {
		if !bytes.Equal(e.Data, msg) {
			return
		}
		if e.Info == nil {
			doneCh <- fmt.Errorf("expecting non nil publication info")
			return
		}
		close(doneCh)
	})

	_ = sub.Subscribe()

	select {
	case <-publishOkCh:
	case <-time.After(5 * time.Second):
		t.Errorf("expecting publication to be successful")
	}

	select {
	case err := <-doneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting publication received over subscription")
	}
}

func TestSubscription_Unsubscribe(t *testing.T) {
	subscribedCh := make(chan struct{}, 1)
	unsubscribedCh := make(chan struct{}, 1)
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	sub, err := client.NewSubscription("test_subscription_close")
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	handler := &testSubscriptionHandler{
		onSubscribe: func(e SubscribedEvent) {
			close(subscribedCh)
		},
		onUnsubscribe: func(event UnsubscribedEvent) {
			close(unsubscribedCh)
		},
	}
	sub.OnUnsubscribed(handler.OnUnsubscribe)
	sub.OnSubscribed(handler.OnSubscribe)
	sub.OnPublication(handler.OnPublication)
	_ = sub.Subscribe()
	select {
	case <-subscribedCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("timeout waiting for subscribe")
	}
	err = sub.Unsubscribe()
	if err != nil {
		t.Fatal("unexpected error", err)
	}
	select {
	case <-unsubscribedCh:
	case <-time.After(3 * time.Second):
		t.Errorf("timeout waiting for subscribe")
	}
}

func TestClient_Publish(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
	_, err := client.Publish(context.Background(), "test", msg)
	if err != nil {
		// Publish should be allowed since we are using Centrifugo in insecure mode in tests.
		t.Fatal(err)
	}
}

func TestClient_Presence(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	_, err := client.Presence(context.Background(), "test")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal("expected protocol error")
	}
	if e.Code != 108 {
		t.Fatal("expected not available error, got " + strconv.FormatUint(uint64(e.Code), 10))
	}
}

func TestClient_PresenceStats(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	_, err := client.PresenceStats(context.Background(), "test")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal("expected protocol error")
	}
	if e.Code != 108 {
		t.Fatal("expected not available error, got " + strconv.FormatUint(uint64(e.Code), 10))
	}
}

func TestClient_History(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close()
	_ = client.Connect()
	channel := "test" + randString(10)
	_, err := client.History(
		context.Background(), channel, WithHistoryReverse(false), WithHistoryLimit(100), WithHistorySince(nil))
	if err != nil {
		t.Fatal("got error", err)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	const (
		numMessages        = 1000
		numResubscritpions = 100
	)

	producer := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer producer.Close()

	if err := producer.Connect(); err != nil {
		t.Fatalf("error on connect: %v", err)
	}

	errChan := make(chan error)
	defer close(errChan)
	go func() {
		for i := 0; i < numMessages; i++ {
			msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
			_, err := producer.Publish(context.Background(), "test_concurrent", msg)
			if err != nil {
				errChan <- fmt.Errorf("error on publish: %v", err)
				return
			}
		}
		errChan <- nil
	}()

	go func() {
		for i := 0; i < numResubscritpions; i++ {
			consumer := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
			if err := consumer.Connect(); err != nil {
				errChan <- fmt.Errorf("error on connect: %v", err)
				return
			}

			handler := &testSubscriptionHandler{}
			sub, err := consumer.NewSubscription("test_concurrent")
			if err != nil {
				errChan <- fmt.Errorf("error on new subscription: %v (%d)", err, i)
				return
			}
			sub.OnSubscribed(handler.OnSubscribe)
			sub.OnPublication(handler.OnPublication)
			if err := sub.Subscribe(); err != nil {
				errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
				return
			}
			sub2, err := consumer.NewSubscription("something_else")
			if err != nil {
				errChan <- fmt.Errorf("error on new subscription: %v (%d)", err, i)
				return
			}
			sub2.OnSubscribed(handler.OnSubscribe)
			sub2.OnPublication(handler.OnPublication)
			if err := sub2.Subscribe(); err != nil {
				errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
				return
			}
		}
		errChan <- nil
	}()

	var err error
	for i := 0; i < 2; i++ {
		if e := <-errChan; e != nil {
			err = e
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPublishSubscribeDisconnect(t *testing.T) {
	// The purpose of this test is to try to catch possible race conditions
	// that can happen when client is disconnected while receiving messages.
	const (
		numMessages        = 1000
		numResubscritpions = 100
	)

	producer := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer producer.Close()

	if err := producer.Connect(); err != nil {
		t.Fatalf("error on connect: %v", err)
	}

	errChan := make(chan error)
	defer close(errChan)
	go func() {
		for i := 0; i < numMessages; i++ {
			msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
			_, err := producer.Publish(context.Background(), "test_concurrent", msg)
			if err != nil {
				errChan <- fmt.Errorf("error on publish: %v", err)
				return
			}
		}
		errChan <- nil
	}()

	go func() {
		for i := 0; i < numResubscritpions; i++ {
			consumer := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
			if err := consumer.Connect(); err != nil {
				errChan <- fmt.Errorf("error on connect: %v", err)
				return
			}

			handler := &testSubscriptionHandler{
				onPublication: func(e PublicationEvent) {
					// We just want the callback queue to do its jobs.
					time.Sleep(time.Microsecond)
				},
			}
			sub, err := consumer.NewSubscription("test_concurrent")
			if err != nil {
				errChan <- fmt.Errorf("error on new subscription: %v (%d)", err, i)
				return
			}
			sub.OnSubscribed(handler.OnSubscribe)
			sub.OnPublication(handler.OnPublication)
			if err := sub.Subscribe(); err != nil {
				errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
				return
			}
			sub2, err := consumer.NewSubscription("something_else")
			if err != nil {
				errChan <- fmt.Errorf("error on new subscription: %v (%d)", err, i)
				return
			}
			sub2.OnSubscribed(handler.OnSubscribe)
			sub2.OnPublication(handler.OnPublication)
			if err := sub2.Subscribe(); err != nil {
				errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
				return
			}
			// Simulate random disconnects.
			go func(cl *Client) {
				time.Sleep(time.Duration(rand.Int63n(150)) * time.Millisecond)
				cl.Close()
			}(consumer)
		}
		errChan <- nil
	}()

	var err error
	for i := 0; i < 2; i++ {
		if e := <-errChan; e != nil {
			err = e
		}
	}
	if err != nil {
		t.Fatal(err)
	}
}

func testFossil(t *testing.T, client *Client) {
	doneCh := make(chan error, 1)
	channel := "test_handle_publish_fossil" + randString(10)
	sub, err := client.NewSubscription(channel, SubscriptionConfig{
		Delta: DeltaTypeFossil,
	})
	if err != nil {
		t.Errorf("error on new subscription: %v", err)
	}
	msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)

	publishOkCh := []chan struct{}{
		make(chan struct{}),
		make(chan struct{}),
	}

	sub.OnSubscribed(func(e SubscribedEvent) {
		if !sub.deltaNegotiated {
			t.Fatal("expecting delta negotiation to be successful")
		}
		go func() {
			for _, ch := range publishOkCh {
				_, err := client.Publish(context.Background(), channel, msg)
				if err != nil {
					t.Fail()
				}
				close(ch)
			}
		}()
	})
	numPublished := 0
	sub.OnPublication(func(e PublicationEvent) {
		if !bytes.Equal(e.Data, msg) {
			return
		}
		numPublished++
		if numPublished == len(publishOkCh) {
			close(doneCh)
		}
	})

	_ = sub.Subscribe()

	for _, ch := range publishOkCh {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("expecting publication to be successful")
		}
	}

	select {
	case err := <-doneCh:
		if err != nil {
			t.Fatalf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("expecting publication received over subscription")
	}
}

func TestHandlePublishFossil(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		client := NewJsonClient("ws://localhost:8000/connection/websocket", Config{})
		defer client.Close()
		_ = client.Connect()
		testFossil(t, client)
	})

	t.Run("protobuf", func(t *testing.T) {
		client := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
		defer client.Close()
		_ = client.Connect()
		testFossil(t, client)
	})
}
