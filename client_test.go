package centrifuge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/centrifugal/centrifuge-go/internal/mutex"
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
	defer client.Close(newCtx(t))
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
	_ = client.Connect(newCtx(t))
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
	defer client.Close(newCtx(t))
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
	_ = client.Connect(newCtx(t))
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
	defer client.Close(newCtx(t))
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
	_ = client.Connect(newCtx(t))
	select {
	case err := <-connectDoneCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Errorf("expecting successful connect")
	}
	_ = client.Disconnect(newCtx(t))
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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
	_, err := client.Publish(context.Background(), "test", []byte("boom"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishJSON(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
	_, err := client.Publish(context.Background(), "test", []byte("{}"))
	if err != nil {
		t.Errorf("error publish: %v", err)
	}
}

func TestPublishInvalidJSON(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
	_, err := client.Publish(context.Background(), "test", []byte("boom"))
	if err == nil {
		t.Errorf("error expected on publish invalid JSON")
	}
}

func TestSubscribeSuccess(t *testing.T) {
	doneCh := make(chan error, 1)
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
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
	_ = sub.Subscribe(newCtx(t))
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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
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
	_ = sub.Subscribe(newCtx(t))
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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
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

	_ = sub.Subscribe(newCtx(t))

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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
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
	_ = sub.Subscribe(newCtx(t))
	select {
	case <-subscribedCh:
		if err != nil {
			t.Errorf("finish with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("timeout waiting for subscribe")
	}
	err = sub.Unsubscribe(newCtx(t))
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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
	msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
	_, err := client.Publish(context.Background(), "test", msg)
	if err != nil {
		// Publish should be allowed since we are using Centrifugo in insecure mode in tests.
		t.Fatal(err)
	}
}

func TestClient_Presence(t *testing.T) {
	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
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
	defer client.Close(newCtx(t))
	_ = client.Connect(newCtx(t))
	_, err := client.History(
		context.Background(), "test", WithHistoryReverse(false), WithHistoryLimit(100), WithHistorySince(nil))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatal("expected protocol error")
	}
	if e.Code != 108 {
		t.Fatal("expected not available error, got " + strconv.FormatUint(uint64(e.Code), 10))
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	const (
		numMessages        = 1000
		numResubscritpions = 100
	)

	client := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
	if err := client.Connect(newCtx(t)); err != nil {
		t.Fatalf("error on connect: %v", err)
	}
	defer func() { client.Close(newCtx(t)) }()
	errChan := make(chan error, numMessages)
	defer close(errChan)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numMessages; i++ {
			msg := []byte(`{"unique":"` + randString(6) + strconv.FormatInt(time.Now().UnixNano(), 10) + `"}`)
			_, err := client.Publish(context.Background(), "test_concurrent", msg)
			if err != nil {
				errChan <- fmt.Errorf("error on publish: %v", err)
				return
			}
		}
		errChan <- nil
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numResubscritpions; i++ {
			func() {
				client2 := NewJsonClient("ws://localhost:8000/connection/websocket?cf_protocol_version=v2", Config{})
				if err := client2.Connect(newCtx(t)); err != nil {
					errChan <- fmt.Errorf("error on connect: %v", err)
					return
				}
				defer func() { client2.Close(newCtx(t)) }()

				handler := &testSubscriptionHandler{}
				sub, err := client2.NewSubscription("test_concurrent")
				if err != nil {
					errChan <- fmt.Errorf("error on new subscription: %v (%d)", err, i)
					return
				}
				sub.OnSubscribed(handler.OnSubscribe)
				sub.OnPublication(handler.OnPublication)
				if err := sub.Subscribe(newCtx(t)); err != nil {
					errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
					return
				}
				sub2, err := client2.NewSubscription("something_else")
				if err != nil {
					errChan <- fmt.Errorf("error on new subscription: %v (%d)", err, i)
					return
				}
				sub2.OnSubscribed(handler.OnSubscribe)
				sub2.OnPublication(handler.OnPublication)
				if err := sub2.Subscribe(newCtx(t)); err != nil {
					errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
					return
				}
			}()
		}
		errChan <- nil
	}()

	wg.Wait()

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
	defer producer.Close(newCtx(t))

	if err := producer.Connect(newCtx(t)); err != nil {
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
			if err := consumer.Connect(newCtx(t)); err != nil {
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
			if err := sub.Subscribe(newCtx(t)); err != nil {
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
			if err := sub2.Subscribe(newCtx(t)); err != nil {
				errChan <- fmt.Errorf("error on subscribe: %v (%d)", err, i)
				return
			}
			// Simulate random disconnects.
			go func(cl *Client) {
				time.Sleep(time.Duration(rand.Int63n(150)) * time.Millisecond)
				cl.Close(newCtx(t))
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

func TestClient_onConnect_respects_context_cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{mu: mutex.New()}
	client.mu.Lock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cancel()
	}()
	err := client.onConnect(ctx, func(err error) {
		t.Fatalf("callback should not be called")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
	wg.Wait()
}

func TestClient_publish_respects_context_cancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{mu: mutex.New()}
	client.mu.Lock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cancel()
	}()
	err := client.publish(ctx, "test", []byte("test"), func(pr PublishResult, err error) {
		t.Fatalf("callback should not be called")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
	wg.Wait()
}

// newCtx registers a cleanup function to cancel the context when the test
// ends. similar to t.Context() in future go versions. can be removed after
// upgrading the module to go1.24.
func newCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

func TestClient_stateEq(t *testing.T) {
	client := &Client{
		mu: mutex.New(),
	}
	client.state = StateClosed
	isClosed, err := client.stateEq(newCtx(t), StateClosed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isClosed {
		t.Fatalf("expected state to be closed")
	}
	client.state = StateDisconnected
	isClosed, err = client.stateEq(newCtx(t), StateClosed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isClosed {
		t.Fatalf("expected state to be disconnected")
	}
}
