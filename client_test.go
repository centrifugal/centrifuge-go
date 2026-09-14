package centrifuge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/centrifugal/protocol"
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
		t.Errorf("timeout waiting for unsubscribe")
	}
}

func TestConcurrentCloseDisconnect(t *testing.T) {
	// Race condition probability between close and disconnect is small,
	// so doing a lot of iterations increase chance of reproducing.
	for i := 0; i < 100; i++ {
		client := NewJsonClient("ws://localhost:8000/connection/websocket", Config{})
		client.OnConnecting(func(ConnectingEvent) {})
		if err := client.Connect(); err != nil {
			t.Fatalf("failed to connect: %v", err)
		}
		sub, err := client.NewSubscription("thechannel", SubscriptionConfig{})
		if err != nil {
			t.Fatalf("failed to subscribe: %v", err)
		}
		sub.OnUnsubscribed(func(UnsubscribedEvent) {})
		sub.OnSubscribing(func(SubscribingEvent) {})
		_ = sub.Subscribe()
		go client.Close()
		client.handleDisconnect(&disconnect{
			Code:      connectingTransportClosed,
			Reason:    "transport closed",
			Reconnect: false,
		})
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

func TestDeltaErrorDisconnectsWithBadProtocol(t *testing.T) {
	brokenDelta := []byte("not a fossil delta")
	for _, tc := range []struct {
		name      string
		recovered bool
		offset    uint64
	}{
		{name: "live publication", offset: 6},
		{name: "recovered publication", recovered: true, offset: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewFakeServer(t)
			s.OnSubscribe = func(ch string, req *protocol.SubscribeRequest) *protocol.SubscribeResult {
				res := &protocol.SubscribeResult{Delta: true, Recoverable: true, Epoch: "e1", Offset: 5}
				if tc.recovered {
					res.WasRecovering = true
					res.Recovered = true
					res.Publications = []*protocol.Publication{{Offset: 5, Data: brokenDelta, Delta: true}}
				}
				return res
			}
			client := NewProtobufClient(s.URL(), Config{})
			defer client.Close()
			errCh := make(chan error, 8)
			client.OnError(func(e ErrorEvent) { errCh <- e.Error })
			disconnectedCh := make(chan DisconnectedEvent, 1)
			client.OnDisconnected(func(e DisconnectedEvent) { disconnectedCh <- e })

			sub, err := client.NewSubscription("news", SubscriptionConfig{Delta: DeltaTypeFossil, Recoverable: true})
			if err != nil {
				t.Fatal(err)
			}
			subscribedCh := make(chan struct{}, 1)
			sub.OnSubscribed(func(SubscribedEvent) { subscribedCh <- struct{}{} })
			publicationCh := make(chan PublicationEvent, 1)
			sub.OnPublication(func(e PublicationEvent) { publicationCh <- e })
			_ = sub.Subscribe()
			_ = client.Connect()

			if !tc.recovered {
				select {
				case <-subscribedCh:
				case <-time.After(3 * time.Second):
					t.Fatal("timeout waiting for subscribed")
				}
				s.SendPush(&protocol.Push{Channel: "news", Pub: &protocol.Publication{Offset: tc.offset, Data: brokenDelta, Delta: true}})
			}

			deadline := time.After(3 * time.Second)
			for found := false; !found; {
				select {
				case err := <-errCh:
					var deltaErr DeltaError
					if errors.As(err, &deltaErr) {
						if deltaErr.Channel != "news" || deltaErr.Offset != tc.offset {
							t.Fatalf("unexpected delta error: %v", deltaErr)
						}
						found = true
					}
				case <-deadline:
					t.Fatal("timeout waiting for DeltaError")
				}
			}
			select {
			case e := <-disconnectedCh:
				if e.Code != disconnectBadProtocol {
					t.Fatalf("expected disconnect code %d, got %d", disconnectBadProtocol, e.Code)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timeout waiting for disconnected")
			}
			select {
			case e := <-publicationCh:
				t.Fatalf("publication with a broken delta delivered: %v", e)
			case <-subscribedCh:
				if tc.recovered {
					t.Fatal("subscribed emitted for a subscribe reply with a broken delta")
				}
			case <-time.After(300 * time.Millisecond):
			}
			if state := client.State(); state != StateDisconnected {
				t.Fatalf("expected client to stay disconnected, got %s", state)
			}
			connects := 0
			for _, cmd := range s.Received() {
				if cmd.Connect != nil {
					connects++
				}
			}
			if connects != 1 {
				t.Fatalf("expected no reconnect, got %d connect commands", connects)
			}
		})
	}
}

func TestApplyDeltaErrorsReturned(t *testing.T) {
	jsonClient := NewJsonClient("ws://localhost:8000/connection/websocket", Config{})
	defer jsonClient.Close()
	protobufClient := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer protobufClient.Close()
	for _, tc := range []struct {
		name   string
		client *Client
		pub    *protocol.Publication
	}{
		// With delta negotiated over JSON, publication data must be a JSON string.
		{name: "json delta not a string", client: jsonClient, pub: &protocol.Publication{Data: []byte(`{"not":"a string"}`), Delta: true}},
		{name: "json data not a string", client: jsonClient, pub: &protocol.Publication{Data: []byte(`{"not":"a string"}`)}},
		{name: "copy without base", client: protobufClient, pub: &protocol.Publication{Data: []byte("3\n3@0,"), Delta: true}},
		// Makes the fossil library slice past the end of the delta.
		{name: "insert past end of delta", client: protobufClient, pub: &protocol.Publication{Data: []byte("3\n3:a"), Delta: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := tc.client.NewSubscription(tc.name, SubscriptionConfig{Delta: DeltaTypeFossil})
			if err != nil {
				t.Fatal(err)
			}
			sub.deltaNegotiated = true
			if _, err := sub.applyDeltaLocked(tc.pub, PublicationEvent{}); err == nil {
				t.Fatalf("expected an error for publication %v", tc.pub)
			}
		})
	}
}

// holdFirstSubscribe makes s wait with its reply to the first subscribe command
// until release is called, like a server under latency: later commands on the
// connection wait behind it, in order. release also runs when the test ends.
func holdFirstSubscribe(t *testing.T, s *FakeServer) (received chan struct{}, release func()) {
	received = make(chan struct{})
	releaseCh := make(chan struct{})
	var releaseOnce, holdOnce sync.Once
	release = func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(release)
	s.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Subscribe != nil {
			held := false
			holdOnce.Do(func() { held = true })
			if held {
				close(received)
				<-releaseCh
			}
		}
		return nil
	}
	return received, release
}

// subscriptionCommands lists the subscribe and unsubscribe commands the server
// received for channel, in order.
func subscriptionCommands(s *FakeServer, channel string) []string {
	var commands []string
	for _, cmd := range s.Received() {
		switch {
		case cmd.Subscribe != nil && cmd.Subscribe.Channel == channel:
			commands = append(commands, "subscribe")
		case cmd.Unsubscribe != nil && cmd.Unsubscribe.Channel == channel:
			commands = append(commands, "unsubscribe")
		}
	}
	return commands
}

// Removing a Subscription that was already removed must not remove a newer
// Subscription to the same channel from the registry: the newer one would then
// not be resubscribed after a reconnect.
func TestRemoveSubscriptionKeepsNewerSubscriptionToSameChannel(t *testing.T) {
	client := NewProtobufClient("ws://127.0.0.1:1/connection/websocket", Config{})
	defer client.Close()

	sub1, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveSubscription(sub1); err != nil {
		t.Fatal(err)
	}
	sub2, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveSubscription(sub1); err != nil {
		t.Fatal(err)
	}
	if sub, ok := client.GetSubscription("news"); !ok || sub != sub2 {
		t.Fatal("removing the stale subscription removed the newer subscription to the same channel")
	}
}

func connectFakeClient(t *testing.T, s *FakeServer, config Config) *Client {
	t.Helper()
	client := NewProtobufClient(s.URL(), config)
	t.Cleanup(client.Close)
	connectedCh := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) {
		select {
		case connectedCh <- struct{}{}:
		default:
		}
	})
	_ = client.Connect()
	waitCh(t, connectedCh, "connected")
	return client
}

func TestSubscribeAfterUnsubscribeWithSubscribePending(t *testing.T) {
	s := NewFakeServer(t)
	subscribeReceived, releaseSubscribe := holdFirstSubscribe(t, s)
	client := connectFakeClient(t, s, Config{})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	subscribedCh := make(chan struct{}, 4)
	sub.OnSubscribed(func(SubscribedEvent) { subscribedCh <- struct{}{} })

	_ = sub.Subscribe()
	waitCh(t, subscribeReceived, "subscribe command")
	_ = sub.Unsubscribe()
	_ = sub.Subscribe()
	releaseSubscribe()
	waitCh(t, subscribedCh, "subscribed")
	time.Sleep(200 * time.Millisecond)

	commands := subscriptionCommands(s, "news")
	if len(commands) == 0 || commands[len(commands)-1] != "subscribe" {
		t.Fatalf("client subscribed, but the server received %v: it has no subscription", commands)
	}
	if state := sub.State(); state != SubStateSubscribed {
		t.Fatalf("expected subscribed, got %s", state)
	}
}

func TestServerUnsubscribeWithSubscribePendingCleansUp(t *testing.T) {
	s := NewFakeServer(t)
	subscribeReceived, releaseSubscribe := holdFirstSubscribe(t, s)
	client := connectFakeClient(t, s, Config{})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	unsubscribedCh := make(chan UnsubscribedEvent, 2)
	sub.OnUnsubscribed(func(e UnsubscribedEvent) { unsubscribedCh <- e })

	_ = sub.Subscribe()
	waitCh(t, subscribeReceived, "subscribe command")
	s.UnsubscribePush("news", 2000, "unsubscribed by server")
	waitCh(t, unsubscribedCh, "unsubscribed")
	releaseSubscribe()
	time.Sleep(200 * time.Millisecond)

	commands := subscriptionCommands(s, "news")
	if len(commands) == 0 || commands[len(commands)-1] != "unsubscribe" {
		t.Fatalf("the server applied the pending subscribe and received %v after it: it keeps a subscription the client doesn't track", commands)
	}
	if state := sub.State(); state != SubStateUnsubscribed {
		t.Fatalf("expected unsubscribed, got %s", state)
	}
}

func TestJoinLeaveNotEmittedWhenNotSubscribed(t *testing.T) {
	s := NewFakeServer(t)
	client := connectFakeClient(t, s, Config{})
	sub, err := client.NewSubscription("news", SubscriptionConfig{JoinLeave: true})
	if err != nil {
		t.Fatal(err)
	}
	subscribedCh := make(chan struct{}, 1)
	sub.OnSubscribed(func(SubscribedEvent) { subscribedCh <- struct{}{} })
	joinCh := make(chan JoinEvent, 4)
	sub.OnJoin(func(e JoinEvent) { joinCh <- e })
	leaveCh := make(chan LeaveEvent, 4)
	sub.OnLeave(func(e LeaveEvent) { leaveCh <- e })
	join := &protocol.Push{Channel: "news", Join: &protocol.Join{Info: &protocol.ClientInfo{Client: "other"}}}
	leave := &protocol.Push{Channel: "news", Leave: &protocol.Leave{Info: &protocol.ClientInfo{Client: "other"}}}

	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")
	s.SendPush(join)
	waitCh(t, joinCh, "join while subscribed")

	_ = sub.Unsubscribe()
	s.SendPush(join)
	s.SendPush(leave)
	time.Sleep(200 * time.Millisecond)
	select {
	case <-joinCh:
		t.Fatal("join emitted after unsubscribe")
	case <-leaveCh:
		t.Fatal("leave emitted after unsubscribe")
	default:
	}
}

func TestSubscriptionCallFailsWhenUnsubscribedWhileSubscribing(t *testing.T) {
	s := NewFakeServer(t)
	subscribeReceived, _ := holdFirstSubscribe(t, s)
	client := connectFakeClient(t, s, Config{ReadTimeout: 3 * time.Second})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	_ = sub.Subscribe()
	waitCh(t, subscribeReceived, "subscribe command")

	resultCh := make(chan error, 1)
	go func() {
		_, err := sub.Publish(context.Background(), []byte(`{}`))
		resultCh <- err
	}()
	// Let Publish wait for the subscription.
	time.Sleep(50 * time.Millisecond)
	_ = sub.Unsubscribe()
	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrSubscriptionUnsubscribed) {
			t.Fatalf("expected ErrSubscriptionUnsubscribed, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Publish still waiting 1s after Unsubscribe()")
	}
}

// Client calls from event handlers, and Close with handlers still queued.
// Handlers run on the callback queue's goroutine, so a call made there must not
// wait for the queue.

// returnsWithin reports whether fn returns within d.
func returnsWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestConnectFromDisconnectedHandler(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{})
	closeOnCleanup(t, client)
	client.OnConnecting(func(ConnectingEvent) {})
	connectedCh := make(chan struct{}, 4)
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })
	handlerReturned := make(chan struct{}, 1)
	var once sync.Once
	client.OnDisconnected(func(DisconnectedEvent) {
		once.Do(func() {
			_ = client.Connect()
			handlerReturned <- struct{}{}
		})
	})
	_ = client.Connect()
	waitCh(t, connectedCh, "connected")

	s.DisconnectPush(3500, "terminal disconnect")
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("Connect() called from OnDisconnected with OnConnecting set never returned")
	}
	waitCh(t, connectedCh, "connected again")
}

func TestConnectFromHandlerWithDialErrorReported(t *testing.T) {
	s := NewFakeServer(t)
	var failDial atomic.Bool
	client := NewProtobufClient(s.URL(), Config{
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if failDial.Load() {
				return nil, errors.New("dial refused")
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		MinReconnectDelay: time.Second,
	})
	closeOnCleanup(t, client)
	errorCh := make(chan error, 8)
	client.OnError(func(e ErrorEvent) {
		select {
		case errorCh <- e.Error:
		default:
		}
	})
	connectedCh := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })
	handlerReturned := make(chan struct{}, 1)
	var once sync.Once
	client.OnDisconnected(func(DisconnectedEvent) {
		once.Do(func() {
			failDial.Store(true)
			_ = client.Connect()
			handlerReturned <- struct{}{}
		})
	})
	_ = client.Connect()
	waitCh(t, connectedCh, "connected")

	s.DisconnectPush(3500, "terminal disconnect")
	select {
	case <-handlerReturned:
	case <-time.After(time.Second):
		t.Fatal("Connect() called from OnDisconnected with a failing dial and OnError set never returned")
	}
	var transportErr TransportError
	if err := waitCh(t, errorCh, "dial error"); !errors.As(err, &transportErr) {
		t.Fatalf("expected TransportError, got %v", err)
	}
}

func TestCloseFromEventHandler(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{})
	closeReturned := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) {
		client.Close()
		closeReturned <- struct{}{}
	})
	_ = client.Connect()
	select {
	case <-closeReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() called from OnConnected never returned")
	}
	if state := client.State(); state != StateClosed {
		t.Fatalf("expected closed, got %s", state)
	}
}

func TestCloseWithQueuedHandlerCallingClient(t *testing.T) {
	s := NewFakeServer(t)
	client := NewProtobufClient(s.URL(), Config{})
	connectedCh := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })
	client.OnDisconnected(func(DisconnectedEvent) {
		// Runs while Close waits for the queued handlers.
		time.Sleep(10 * time.Millisecond)
		_ = client.State()
	})
	_ = client.Connect()
	waitCh(t, connectedCh, "connected")

	if !returnsWithin(2*time.Second, client.Close) {
		t.Fatal("Close() blocked while a queued OnDisconnected handler called client.State()")
	}
}

// failingWriteTransport fails every write after running beforeFail, a hook to
// interleave a teardown with the failing write.
type failingWriteTransport struct {
	beforeFail func()
}

func (t *failingWriteTransport) Read() (*protocol.Reply, *disconnect, error) {
	select {}
}

func (t *failingWriteTransport) Write(*protocol.Command, time.Duration) error {
	if t.beforeFail != nil {
		t.beforeFail()
	}
	return errors.New("write failed")
}

func (t *failingWriteTransport) Close() error {
	return nil
}

func TestCallReturnsWhenSendFailsDuringTeardown(t *testing.T) {
	client := NewProtobufClient("ws://127.0.0.1:1/connection/websocket", Config{
		MinReconnectDelay: 10 * time.Second,
		MaxReconnectDelay: 20 * time.Second,
	})
	closeOnCleanup(t, client)
	ft := &failingWriteTransport{}
	ft.beforeFail = func() {
		// The connection is torn down while the command is written: the
		// teardown fails every pending request, this one included.
		client.mu.Lock()
		client.clearConnectedState()
		client.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
	}
	client.mu.Lock()
	client.state = StateConnected
	client.setTransportLocked(ft)
	client.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := client.Publish(ctx, "ch", []byte(`{}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error for a publish whose send failed")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Publish never returned after its send failed during a teardown, despite its 1s context")
	}
}

func TestSendDoesNotRaceWithDisconnect(t *testing.T) {
	client := NewProtobufClient("ws://127.0.0.1:1/connection/websocket", Config{
		MinReconnectDelay: 10 * time.Second,
		MaxReconnectDelay: 20 * time.Second,
	})
	closeOnCleanup(t, client)
	client.mu.Lock()
	client.state = StateConnected
	client.setTransportLocked(noopTransport{})
	client.mu.Unlock()

	// send runs without the client lock (Send, calls of subscriptions) while
	// Disconnect clears the transport. The goroutine shares no lock with the
	// test, so with -race an unsynchronized read of the transport is reported
	// whatever the timing.
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		close(ready)
		for i := 0; i < 100; i++ {
			_ = client.send(&protocol.Command{Send: &protocol.SendRequest{Data: []byte(`{}`)}})
		}
	}()
	<-ready
	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestSubscribeReplyDoesNotHoldSubscriptionLockForWaitingCalls(t *testing.T) {
	s := NewFakeServer(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseReply := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseReply)
	s.OnSubscribe = func(string, *protocol.SubscribeRequest) *protocol.SubscribeResult {
		<-release
		return &protocol.SubscribeResult{}
	}
	client := connectFakeClient(t, s, Config{})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	_ = sub.Subscribe()

	publishDone := make(chan error, 1)
	go func() {
		_, err := sub.Publish(context.Background(), []byte(`{}`))
		publishDone <- err
	}()
	for start := time.Now(); ; time.Sleep(time.Millisecond) {
		sub.mu.Lock()
		waiting := len(sub.subFutures)
		sub.mu.Unlock()
		if waiting == 1 && s.LastSubscribe() != nil {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("publish isn't waiting for the pending subscribe")
		}
	}

	// Hold the client lock, as Close does before it locks each subscription,
	// while the subscribe reply resolves the waiting publish.
	client.mu.Lock()
	releaseReply()
	var blocked, subscribed bool
	for deadline := time.Now().Add(2 * time.Second); !blocked && !subscribed && time.Now().Before(deadline); {
		stateCh := make(chan SubState, 1)
		go func() { stateCh <- sub.State() }()
		select {
		case state := <-stateCh:
			subscribed = state == SubStateSubscribed
		case <-time.After(time.Second):
			blocked = true
		}
	}
	client.mu.Unlock()
	if blocked {
		t.Fatal("the subscribe reply holds the subscription lock while the waiting publish waits for the client lock: Close would deadlock with it")
	}
	if !subscribed {
		t.Fatal("subscription not subscribed")
	}
	if err := waitCh(t, publishDone, "publish"); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveredPublicationsOfEndedSessionNotDeliveredAgain(t *testing.T) {
	s := NewFakeServer(t)
	var subscribes atomic.Int32
	s.OnSubscribe = func(_ string, req *protocol.SubscribeRequest) *protocol.SubscribeResult {
		if subscribes.Add(1) == 1 {
			return &protocol.SubscribeResult{Recoverable: true, Epoch: "e1", Offset: 5}
		}
		// Like Centrifugo, a successful recovery replies with the requested
		// position and the publications after it.
		return &protocol.SubscribeResult{
			Recoverable: true, Epoch: "e1", Offset: req.Offset, WasRecovering: true, Recovered: true,
			Publications: []*protocol.Publication{
				{Offset: 6, Data: []byte(`{"n":6}`)},
				{Offset: 7, Data: []byte(`{"n":7}`)},
			},
		}
	}
	// No OnConnected handler: while the subscribed handler below is blocked, a
	// newer connection must get as far as resubscribing, and its connected event
	// would wait for that handler.
	client := NewProtobufClient(s.URL(), Config{MinReconnectDelay: 10 * time.Millisecond, MaxReconnectDelay: 20 * time.Millisecond})
	closeOnCleanup(t, client)
	_ = client.Connect()
	waitCondition(t, "connected", func() bool { return client.State() == StateConnected })
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseHandler)
	subscribed := make(chan struct{}, 4)
	var subscribedEvents atomic.Int32
	sub.OnSubscribed(func(SubscribedEvent) {
		if subscribedEvents.Add(1) == 2 {
			// The subscribed handler of the first recovery is slow.
			entered <- struct{}{}
			<-release
		}
		subscribed <- struct{}{}
	})
	var mu sync.Mutex
	var delivered []string
	sub.OnPublication(func(e PublicationEvent) {
		mu.Lock()
		delivered = append(delivered, string(e.Data))
		mu.Unlock()
	})

	_ = sub.Subscribe()
	waitCh(t, subscribed, "subscribed")
	_ = client.Disconnect()
	_ = client.Connect()
	waitCh(t, entered, "the subscribed handler of the first recovery")

	// Meanwhile the connection is replaced, and the next subscribed session
	// recovers the same publications.
	_ = client.Disconnect()
	_ = client.Connect()
	waitCondition(t, "the second recovery", func() bool {
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.subscribedSession == 3
	})
	releaseHandler()
	waitCh(t, subscribed, "the first recovery's subscribed event")
	waitCh(t, subscribed, "the second recovery's subscribed event")
	waitCondition(t, "recovered publications", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(delivered) >= 2
	})
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(delivered) != 2 || delivered[0] != `{"n":6}` || delivered[1] != `{"n":7}` {
		t.Fatalf("recovered publications must be delivered once, got %v", delivered)
	}
}

func TestSubscribeRacingUnsubscribeIsNotRejectedAsAlreadySubscribed(t *testing.T) {
	s := NewFakeServer(t)
	var serverMu sync.Mutex
	serverSubscribed := false
	var rejected atomic.Int32
	s.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		serverMu.Lock()
		defer serverMu.Unlock()
		switch {
		case cmd.Subscribe != nil:
			// Like Centrifugo: a subscribe to a channel the connection is
			// subscribed to already is rejected with 105.
			if serverSubscribed {
				rejected.Add(1)
				return &protocol.Reply{Id: cmd.Id, Error: &protocol.Error{Code: 105, Message: "already subscribed"}}
			}
			serverSubscribed = true
		case cmd.Unsubscribe != nil:
			serverSubscribed = false
		}
		return nil
	}
	client := connectFakeClient(t, s, Config{})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	subscribed := make(chan struct{}, 64)
	sub.OnSubscribed(func(SubscribedEvent) {
		select {
		case subscribed <- struct{}{}:
		default:
		}
	})
	waitSubscribed := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for sub.State() != SubStateSubscribed {
			if rejected.Load() > 0 {
				// The server still has the subscription: reported below.
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("not subscribed, state %s", sub.State())
			}
			if sub.State() == SubStateUnsubscribed {
				_ = sub.Subscribe()
			}
			select {
			case <-subscribed:
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	// Unsubscribe and Subscribe from two goroutines at once, many times: the
	// subscribe must never reach the server before the unsubscribe.
	for i := 0; i < 3000 && rejected.Load() == 0; i++ {
		waitSubscribed()
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = sub.Unsubscribe()
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = sub.Subscribe()
		}()
		close(start)
		wg.Wait()
	}
	if n := rejected.Load(); n > 0 {
		t.Fatalf("a subscribe reached the server before the unsubscribe of the same subscription and was rejected as already subscribed (%d times)", n)
	}
}

func TestCloseUnsubscribesThenReportsDisconnect(t *testing.T) {
	s := NewFakeServer(t)
	s.ConnectResult = &protocol.ConnectResult{
		Client: "fake-client",
		Subs:   map[string]*protocol.SubscribeResult{"server-side": {}},
	}
	client := NewProtobufClient(s.URL(), Config{})
	t.Cleanup(client.Close)

	var closing atomic.Bool
	events := make(chan string, 8)
	record := func(event string) {
		if closing.Load() {
			events <- event
		}
	}
	connected := make(chan struct{}, 1)
	client.OnConnected(func(ConnectedEvent) {
		select {
		case connected <- struct{}{}:
		default:
		}
	})
	client.OnDisconnected(func(DisconnectedEvent) { record("client disconnected") })
	client.OnSubscribing(func(ServerSubscribingEvent) { record("server-side subscribing") })
	client.OnUnsubscribed(func(ServerUnsubscribedEvent) { record("server-side unsubscribed") })
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	subscribed := make(chan struct{}, 1)
	sub.OnSubscribed(func(SubscribedEvent) {
		select {
		case subscribed <- struct{}{}:
		default:
		}
	})
	sub.OnSubscribing(func(SubscribingEvent) { record("sub subscribing") })
	sub.OnUnsubscribed(func(UnsubscribedEvent) { record("sub unsubscribed") })

	_ = client.Connect()
	waitCh(t, connected, "connected")
	_ = sub.Subscribe()
	waitCh(t, subscribed, "subscribed")

	closing.Store(true)
	client.Close()
	close(events)
	var got []string
	for event := range events {
		got = append(got, event)
	}
	// Close unsubscribes, then reports the disconnect, without moving the
	// subscriptions through subscribing.
	want := []string{"sub unsubscribed", "server-side unsubscribed", "client disconnected"}
	if len(got) != len(want) {
		t.Fatalf("events of Close: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events of Close: got %v, want %v", got, want)
		}
	}
	if state := client.State(); state != StateClosed {
		t.Fatalf("expected closed state, got %s", state)
	}
}

func TestOutdatedSubscribeErrorDoesNotUnsubscribeNewAttempt(t *testing.T) {
	s := NewFakeServer(t)
	var subscribes atomic.Int32
	s.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Subscribe != nil && subscribes.Add(1) == 1 {
			return &protocol.Reply{Id: cmd.Id, Error: &protocol.Error{Code: 103, Message: "permission denied"}}
		}
		return nil
	}
	client := connectFakeClient(t, s, Config{})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	sub.OnError(func(SubscriptionErrorEvent) {
		// While the first subscribe's permanent error is handled, the
		// application unsubscribes and subscribes again.
		once.Do(func() {
			_ = sub.Unsubscribe()
			_ = sub.Subscribe()
		})
	})
	subscribed := make(chan struct{}, 1)
	sub.OnSubscribed(func(SubscribedEvent) {
		select {
		case subscribed <- struct{}{}:
		default:
		}
	})
	_ = sub.Subscribe()
	waitCh(t, subscribed, "subscribed by the new attempt")
	time.Sleep(50 * time.Millisecond)
	if state := sub.State(); state != SubStateSubscribed {
		t.Fatalf("the error of the old subscribe was applied to the new attempt: state %s", state)
	}
}

func TestCloseDuringConnectFailsWaitingCallsAndReportsDisconnect(t *testing.T) {
	s := NewFakeServer(t)
	connectReceived := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseConnect := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseConnect)
	s.OnCommand = func(cmd *protocol.Command) *protocol.Reply {
		if cmd.Connect != nil {
			select {
			case connectReceived <- struct{}{}:
			default:
			}
			<-release
		}
		return nil
	}
	client := NewProtobufClient(s.URL(), Config{ReadTimeout: 3 * time.Second})
	events := make(chan string, 4)
	client.OnDisconnected(func(DisconnectedEvent) { events <- "disconnected" })
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	sub.OnUnsubscribed(func(UnsubscribedEvent) { events <- "unsubscribed" })
	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, connectReceived, "connect")

	callErr := make(chan error, 1)
	client.onConnect(func(err error) { callErr <- err })
	client.Close()
	select {
	case err := <-callErr:
		if !errors.Is(err, ErrClientDisconnected) {
			t.Fatalf("expected ErrClientDisconnected for a call waiting for the connect, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a call waiting for the connect wasn't failed by Close")
	}
	// Close unsubscribes first, then reports the disconnect.
	if ev := waitCh(t, events, "the unsubscribed event"); ev != "unsubscribed" {
		t.Fatalf("expected the unsubscribed event first, got %s", ev)
	}
	if ev := waitCh(t, events, "the disconnected event"); ev != "disconnected" {
		t.Fatalf("expected the disconnected event after unsubscribed, got %s", ev)
	}

	// A call racing with Close that gets to the client after it.
	client.onConnect(func(err error) { callErr <- err })
	select {
	case err := <-callErr:
		if !errors.Is(err, ErrClientClosed) {
			t.Fatalf("expected ErrClientClosed for a call after Close, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a call after Close waits instead of failing")
	}
}

func TestCallCompletesWithReplyReceivedBeforeTimeout(t *testing.T) {
	s := NewFakeServer(t)
	const readTimeout = 150 * time.Millisecond
	client := connectFakeClient(t, s, Config{ReadTimeout: readTimeout})
	sub, err := client.NewSubscription("news")
	if err != nil {
		t.Fatal(err)
	}
	subscribedCh := make(chan struct{}, 1)
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
	waitCh(t, subscribedCh, "subscribed")

	// Keep the reader busy, like a suspended process: the publish reply waits
	// unprocessed while the call's timeout fires.
	s.PublishChannel("news", []byte(`{}`))
	waitCh(t, inHandler, "publication handler")
	resultCh := make(chan error, 1)
	go func() {
		_, err := client.Publish(context.Background(), "x", []byte(`{}`))
		resultCh <- err
	}()
	time.Sleep(readTimeout + 25*time.Millisecond)
	close(releaseHandler)

	if err := waitCh(t, resultCh, "publish result"); err != nil {
		t.Fatalf("publish failed with %v although its reply was received when the timeout fired", err)
	}
}

// Recovery against the real server: after a successful recovery Centrifugo sends
// the requested position as the reply's offset, so the stored position has to
// come from the recovered publications. Publishing only while the client is
// disconnected makes a position that goes back show up as duplicates on the
// next recovery.
func TestRepeatedRecoveryWithoutLivePublicationsHasNoDuplicates(t *testing.T) {
	channel := "test_repeated_recovery_" + randString(10)
	publisher := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer publisher.Close()
	_ = publisher.Connect()

	client := NewProtobufClient("ws://localhost:8000/connection/websocket", fastReconnectConfig())
	defer client.Close()
	sub, err := client.NewSubscription(channel, SubscriptionConfig{Recoverable: true})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var received []int
	sub.OnPublication(func(e PublicationEvent) {
		var n int
		if _, err := fmt.Sscanf(string(e.Data), `{"n":%d}`, &n); err != nil {
			t.Errorf("unexpected publication data %q", e.Data)
			return
		}
		mu.Lock()
		received = append(received, n)
		mu.Unlock()
	})
	subscribedCh := make(chan SubscribedEvent, 8)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	published := 0
	for round := 1; round <= 3; round++ {
		if err := client.Disconnect(); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			published++
			if _, err := publisher.Publish(context.Background(), channel, []byte(fmt.Sprintf(`{"n":%d}`, published))); err != nil {
				t.Fatalf("publish: %v", err)
			}
		}
		_ = client.Connect()
		if ev := waitCh(t, subscribedCh, "resubscribed"); !ev.WasRecovering || !ev.Recovered {
			t.Fatalf("round %d: expected a successful recovery, got %+v", round, ev)
		}
		waitCondition(t, "recovered publications", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(received) >= published
		})
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(received) != published {
		t.Fatalf("expected %d publications, each once, got %v", published, received)
	}
	for i, n := range received {
		if n != i+1 {
			t.Fatalf("publications out of order or duplicated: %v", received)
		}
	}
}

// testToken returns an HS256 connection token signed with the secret of the
// docker-compose Centrifugo.
func testToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	encode := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	unsigned := encode(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." + encode(claims)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// testServerAPI calls the server API of the docker-compose Centrifugo and
// returns the result.
func testServerAPI(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://localhost:8000/api/"+method, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("api %s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		t.Fatalf("api %s: status %d: %v", method, resp.StatusCode, err)
	}
	if reply.Error != nil {
		t.Fatalf("api %s: %d %s", method, reply.Error.Code, reply.Error.Message)
	}
	return reply.Result
}

// testServerVersion returns the version of the docker-compose Centrifugo.
func testServerVersion(t *testing.T) (major, minor, patch int) {
	t.Helper()
	var info struct {
		Nodes []struct {
			Version string `json:"version"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(testServerAPI(t, "info", map[string]any{}), &info); err != nil || len(info.Nodes) == 0 {
		t.Fatalf("unexpected info result: %v", err)
	}
	if _, err := fmt.Sscanf(info.Nodes[0].Version, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("unexpected server version %q: %v", info.Nodes[0].Version, err)
	}
	return major, minor, patch
}

func TestUnrecoverablePositionWithoutGetStateReportsNotRecovered(t *testing.T) {
	channel := "smallhistory:test_unrecoverable_" + randString(10)
	publisher := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer publisher.Close()
	_ = publisher.Connect()

	client := NewProtobufClient("ws://localhost:8000/connection/websocket", fastReconnectConfig())
	defer client.Close()
	sub, err := client.NewSubscription(channel, SubscriptionConfig{Recoverable: true})
	if err != nil {
		t.Fatal(err)
	}
	publications := make(chan string, 16)
	sub.OnPublication(func(e PublicationEvent) { publications <- string(e.Data) })
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")

	publish := func(data string) {
		t.Helper()
		if _, err := publisher.Publish(context.Background(), channel, []byte(data)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	publish(`{"n":1}`)
	if got := waitCh(t, publications, "live publication"); got != `{"n":1}` {
		t.Fatalf("unexpected publication %s", got)
	}

	// The channel's history keeps 2 publications: 5 published while
	// disconnected make the position unrecoverable.
	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for n := 2; n <= 6; n++ {
		publish(fmt.Sprintf(`{"n":%d}`, n))
	}
	_ = client.Connect()
	if ev := waitCh(t, subscribedCh, "resubscribed"); !ev.WasRecovering || ev.Recovered {
		t.Fatalf("expected a failed recovery (WasRecovering without Recovered), got %+v", ev)
	}

	publish(`{"n":7}`)
	if got := waitCh(t, publications, "live publication after the failed recovery"); got != `{"n":7}` {
		t.Fatalf("expected only live publications after a failed recovery, got %s", got)
	}
	if state := sub.State(); state != SubStateSubscribed {
		t.Fatalf("expected subscribed, got %s", state)
	}
}

// deltaTestPayload returns publication data that differs little between
// consecutive n, so the server sends fossil deltas.
func deltaTestPayload(n int) string {
	return fmt.Sprintf(`{"n":%d,"text":"%s"}`, n, strings.Repeat("centrifugo ", 20))
}

func TestRecoveryWithFossilDeltaDeliversExactData(t *testing.T) {
	channel := "test_delta_recovery_" + randString(10)
	publisher := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer publisher.Close()
	_ = publisher.Connect()

	client := NewProtobufClient("ws://localhost:8000/connection/websocket", fastReconnectConfig())
	defer client.Close()
	errCh := make(chan error, 4)
	client.OnError(func(e ErrorEvent) {
		select {
		case errCh <- e.Error:
		default:
		}
	})
	sub, err := client.NewSubscription(channel, SubscriptionConfig{Recoverable: true, Delta: DeltaTypeFossil})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var received []string
	sub.OnPublication(func(e PublicationEvent) {
		mu.Lock()
		received = append(received, string(e.Data))
		mu.Unlock()
	})
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed")
	sub.mu.Lock()
	negotiated := sub.deltaNegotiated
	sub.mu.Unlock()
	if !negotiated {
		t.Fatal("fossil delta wasn't negotiated")
	}

	var expected []string
	publish := func(n int) {
		t.Helper()
		data := deltaTestPayload(n)
		expected = append(expected, data)
		if _, err := publisher.Publish(context.Background(), channel, []byte(data)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	waitReceived := func(label string) {
		t.Helper()
		waitCondition(t, label, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(received) >= len(expected)
		})
	}

	for n := 1; n <= 3; n++ {
		publish(n)
	}
	waitReceived("live publications")
	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for n := 4; n <= 6; n++ {
		publish(n)
	}
	_ = client.Connect()
	if ev := waitCh(t, subscribedCh, "resubscribed"); !ev.WasRecovering || !ev.Recovered {
		t.Fatalf("expected a successful recovery, got %+v", ev)
	}
	waitReceived("recovered publications")
	for n := 7; n <= 9; n++ {
		publish(n)
	}
	waitReceived("live publications after the recovery")
	time.Sleep(100 * time.Millisecond)

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != len(expected) {
		t.Fatalf("expected %d publications, got %d", len(expected), len(received))
	}
	for i := range expected {
		if received[i] != expected[i] {
			t.Fatalf("publication %d decoded to %q, published %q", i+1, received[i], expected[i])
		}
	}
}

// A recovery that returns no publications, from a position whose publication
// the client never received: the next publication must not be a delta against
// it (centrifugal/centrifuge#629).
func TestDeltaAfterRecoveryWithoutPublications(t *testing.T) {
	if major, minor, patch := testServerVersion(t); major < 6 || (major == 6 && (minor < 9 || (minor == 9 && patch < 6))) {
		t.Skipf("needs Centrifugo 6.9.6 or newer (centrifugal/centrifuge#629), got %d.%d.%d", major, minor, patch)
	}
	channel := "test_delta_empty_recovery_" + randString(10)
	publisher := NewProtobufClient("ws://localhost:8000/connection/websocket", Config{})
	defer publisher.Close()
	_ = publisher.Connect()
	if _, err := publisher.Publish(context.Background(), channel, []byte(deltaTestPayload(1))); err != nil {
		t.Fatalf("publish: %v", err)
	}

	client := NewProtobufClient("ws://localhost:8000/connection/websocket", fastReconnectConfig())
	defer client.Close()
	errCh := make(chan error, 4)
	client.OnError(func(e ErrorEvent) {
		select {
		case errCh <- e.Error:
		default:
		}
	})
	sub, err := client.NewSubscription(channel, SubscriptionConfig{Recoverable: true, Delta: DeltaTypeFossil})
	if err != nil {
		t.Fatal(err)
	}
	publications := make(chan string, 4)
	sub.OnPublication(func(e PublicationEvent) { publications <- string(e.Data) })
	subscribedCh := make(chan SubscribedEvent, 4)
	sub.OnSubscribed(func(e SubscribedEvent) { subscribedCh <- e })
	_ = client.Connect()
	_ = sub.Subscribe()
	waitCh(t, subscribedCh, "subscribed at the stream top")

	if err := client.Disconnect(); err != nil {
		t.Fatal(err)
	}
	_ = client.Connect()
	if ev := waitCh(t, subscribedCh, "resubscribed"); !ev.WasRecovering || !ev.Recovered {
		t.Fatalf("expected a successful recovery, got %+v", ev)
	}
	if _, err := publisher.Publish(context.Background(), channel, []byte(deltaTestPayload(2))); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case got := <-publications:
		if got != deltaTestPayload(2) {
			t.Fatalf("publication decoded to %q, published %q", got, deltaTestPayload(2))
		}
	case err := <-errCh:
		// E.g. a DeltaError: the server sent a delta against a publication the
		// client never received.
		t.Fatalf("error instead of the publication after the recovery: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the publication after the recovery")
	}
	if state := client.State(); state != StateConnected {
		t.Fatalf("expected connected, got %s", state)
	}
}

// fastReconnectConfig returns a Config with short reconnect delays so stress
// tests do not spend most of their time waiting for backoff timers.
func fastReconnectConfig() Config {
	return Config{
		MinReconnectDelay: 50 * time.Millisecond,
		MaxReconnectDelay: 200 * time.Millisecond,
		ReadTimeout:       2 * time.Second,
		HandshakeTimeout:  time.Second,
	}
}

// TestStress_CloseWhileReconnecting verifies that Close() called while the
// client is in a reconnect loop (bad server address) always completes promptly
// and never hangs. This is the scenario that triggered issue #105: the cbQueue
// dispatch goroutine must not stall between reconnect attempts.
func TestStress_CloseWhileReconnecting(t *testing.T) {
	const iterations = 30
	for i := 0; i < iterations; i++ {
		client := NewJsonClient("ws://localhost:9000/connection/websocket", fastReconnectConfig())
		client.OnConnecting(func(ConnectingEvent) {})
		client.OnError(func(ErrorEvent) {})
		_ = client.Connect()

		// Randomise how long the reconnect loop runs before we shut it down.
		time.Sleep(time.Duration(rand.Intn(15)) * time.Millisecond)

		done := make(chan struct{})
		go func() {
			client.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Close() hung while client was reconnecting", i)
		}
	}
}

// TestStress_ConcurrentCloseWhileReconnecting is the same scenario as above
// but Close() is called from several goroutines simultaneously to surface
// races on the client state machine and the cbQueue.
func TestStress_ConcurrentCloseWhileReconnecting(t *testing.T) {
	const iterations = 20
	const closers = 5
	for i := 0; i < iterations; i++ {
		client := NewJsonClient("ws://localhost:9000/connection/websocket", fastReconnectConfig())
		client.OnConnecting(func(ConnectingEvent) {})
		client.OnError(func(ErrorEvent) {})
		_ = client.Connect()
		time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)

		var wg sync.WaitGroup
		for j := 0; j < closers; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				client.Close()
			}()
		}
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: concurrent Close() calls hung", i)
		}
	}
}

// TestStress_RepeatedForcedReconnects connects to a real server and forces N
// disconnect-then-reconnect cycles, waiting for a successful reconnect each
// time. It verifies the reconnect path — including cbQueue handler dispatch —
// completes reliably under repeated churn and that every cycle fires exactly
// one connected event.
func TestStress_RepeatedForcedReconnects(t *testing.T) {
	const cycles = 25

	var connectedCount atomic.Int32
	connectedCh := make(chan struct{}, cycles+1)

	client := NewJsonClient(
		"ws://localhost:8000/connection/websocket?cf_protocol_version=v2",
		fastReconnectConfig(),
	)
	defer client.Close()
	client.OnConnecting(func(ConnectingEvent) {})
	client.OnConnected(func(ConnectedEvent) {
		connectedCount.Add(1)
		connectedCh <- struct{}{}
	})
	client.OnError(func(ErrorEvent) {})

	_ = client.Connect()
	select {
	case <-connectedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("initial connect timed out")
	}

	for i := 0; i < cycles; i++ {
		client.handleDisconnect(&disconnect{
			Code:      connectingTransportClosed,
			Reason:    "stress-test-forced-disconnect",
			Reconnect: true,
		})
		select {
		case <-connectedCh:
		case <-time.After(10 * time.Second):
			t.Fatalf("cycle %d: reconnect timed out", i)
		}
	}

	if got := int(connectedCount.Load()); got != cycles+1 {
		t.Fatalf("expected %d connected events, got %d", cycles+1, got)
	}
}

// TestStress_AllHandlersDuringReconnect registers every available client
// handler, forces many reconnect cycles and verifies that the handler call
// counts are self-consistent: every connecting event must be followed by
// exactly one connected event, and the cbQueue must never stall even with
// all handler slots occupied.
func TestStress_AllHandlersDuringReconnect(t *testing.T) {
	const cycles = 20

	var (
		connectingCount atomic.Int32
		connectedCount  atomic.Int32
		errorCount      atomic.Int32
	)
	connectedCh := make(chan struct{}, cycles+1)

	client := NewJsonClient(
		"ws://localhost:8000/connection/websocket?cf_protocol_version=v2",
		fastReconnectConfig(),
	)
	defer client.Close()

	client.OnConnecting(func(ConnectingEvent) { connectingCount.Add(1) })
	client.OnConnected(func(ConnectedEvent) {
		connectedCount.Add(1)
		connectedCh <- struct{}{}
	})
	client.OnDisconnected(func(DisconnectedEvent) {})
	client.OnError(func(e ErrorEvent) { errorCount.Add(1) })

	_ = client.Connect()
	select {
	case <-connectedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("initial connect timed out")
	}

	for i := 0; i < cycles; i++ {
		client.handleDisconnect(&disconnect{
			Code:      connectingTransportClosed,
			Reason:    "stress-test-forced-disconnect",
			Reconnect: true,
		})
		select {
		case <-connectedCh:
		case <-time.After(10 * time.Second):
			t.Fatalf("cycle %d: reconnect timed out (connecting=%d connected=%d)",
				i, connectingCount.Load(), connectedCount.Load())
		}
	}

	if got, want := int(connectedCount.Load()), cycles+1; got != want {
		t.Fatalf("connected events: got %d, want %d", got, want)
	}
	// Every reconnect fires a connecting event; the initial Connect() also does.
	if got := int(connectingCount.Load()); got < cycles+1 {
		t.Fatalf("connecting events: got %d, want at least %d", got, cycles+1)
	}
}

// TestStress_ReconnectWithSlowHandlers verifies that the reconnect loop
// completes even when every handler sleeps briefly. This guards against
// regressions where a slow handler permanently stalls runHandlerSync and
// prevents scheduleReconnectLocked from being reached.
func TestStress_ReconnectWithSlowHandlers(t *testing.T) {
	const cycles = 10
	const handlerDelay = 5 * time.Millisecond

	connectedCh := make(chan struct{}, cycles+1)

	client := NewJsonClient(
		"ws://localhost:8000/connection/websocket?cf_protocol_version=v2",
		fastReconnectConfig(),
	)
	defer client.Close()

	client.OnConnecting(func(ConnectingEvent) { time.Sleep(handlerDelay) })
	client.OnConnected(func(ConnectedEvent) {
		time.Sleep(handlerDelay)
		connectedCh <- struct{}{}
	})
	client.OnError(func(ErrorEvent) { time.Sleep(handlerDelay) })

	_ = client.Connect()
	select {
	case <-connectedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("initial connect timed out")
	}

	for i := 0; i < cycles; i++ {
		client.handleDisconnect(&disconnect{
			Code:      connectingTransportClosed,
			Reason:    "stress-test-forced-disconnect",
			Reconnect: true,
		})
		// Allow extra time per cycle for the handler delays.
		select {
		case <-connectedCh:
		case <-time.After(15 * time.Second):
			t.Fatalf("cycle %d: reconnect timed out with slow handlers", i)
		}
	}
}

// TestStress_ReconnectWithSubscriptions subscribes to a channel, forces many
// reconnects and verifies that the subscription resubscribes successfully after
// each one. This exercises the interaction between the reconnect path, the
// cbQueue and subscription state management under churn.
func TestStress_ReconnectWithSubscriptions(t *testing.T) {
	const cycles = 15

	connectedCh := make(chan struct{}, cycles+1)
	subscribedCh := make(chan struct{}, cycles+1)

	client := NewJsonClient(
		"ws://localhost:8000/connection/websocket?cf_protocol_version=v2",
		fastReconnectConfig(),
	)
	defer client.Close()

	sub, err := client.NewSubscription("stress_reconnect_sub")
	if err != nil {
		t.Fatal(err)
	}
	sub.OnSubscribed(func(SubscribedEvent) { subscribedCh <- struct{}{} })
	sub.OnSubscribing(func(SubscribingEvent) {})
	sub.OnUnsubscribed(func(UnsubscribedEvent) {})

	client.OnConnecting(func(ConnectingEvent) {})
	client.OnConnected(func(ConnectedEvent) { connectedCh <- struct{}{} })
	client.OnError(func(ErrorEvent) {})

	_ = client.Connect()
	_ = sub.Subscribe()

	select {
	case <-connectedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("initial connect timed out")
	}
	select {
	case <-subscribedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("initial subscribe timed out")
	}

	for i := 0; i < cycles; i++ {
		client.handleDisconnect(&disconnect{
			Code:      connectingTransportClosed,
			Reason:    "stress-test-forced-disconnect",
			Reconnect: true,
		})
		select {
		case <-connectedCh:
		case <-time.After(10 * time.Second):
			t.Fatalf("cycle %d: reconnect timed out", i)
		}
		select {
		case <-subscribedCh:
		case <-time.After(10 * time.Second):
			t.Fatalf("cycle %d: resubscribe timed out", i)
		}
	}
}

func TestLogLevel(t *testing.T) {
	cases := []struct {
		name            string
		configuredLevel LogLevel
		requestedLevel  LogLevel
		enabled         bool
	}{
		{
			"configured with debug, requested trace",
			LogLevelDebug,
			LogLevelTrace,
			false,
		},
		{
			"configured with none, requested trace",
			LogLevelNone,
			LogLevelTrace,
			false,
		},
		{
			"configured with none, requested dabug",
			LogLevelNone,
			LogLevelDebug,
			false,
		},
		{
			"configured with trace, requested debug",
			LogLevelTrace,
			LogLevelDebug,
			true,
		},
		{
			"configured with debug, requested debug",
			LogLevelDebug,
			LogLevelDebug,
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := NewJsonClient("ws://localhost:9000/connection/websocket", Config{
				LogLevel: tc.configuredLevel,
			})

			got := client.logLevelEnabled(tc.requestedLevel)
			if got != tc.enabled {
				t.Errorf("expected %v got %v", tc.enabled, got)
			}
		})
	}
}

// noopTransport is a transport whose Write always succeeds without actually
// sending anything anywhere, so sendAsync's ReadTimeout goroutine gets
// started without needing a real connection.
type noopTransport struct{}

func (noopTransport) Read() (*protocol.Reply, *disconnect, error)  { return nil, nil, io.EOF }
func (noopTransport) Write(*protocol.Command, time.Duration) error { return nil }
func (noopTransport) Close() error                                 { return nil }

// TestClient_RequestCallbackNotInvokedTwiceOnTimeoutRace is a regression test
// for a race where a reply arriving at almost the same moment its ReadTimeout
// fired could invoke the same request's callback twice: once from handle()
// with the real reply, once from the sendAsync timeout goroutine with
// ErrTimeout. Since callers resolve via a buffered channel of size 1, the
// second invocation used to block forever, leaking a goroutine.
//
// The callback below sleeps briefly after being invoked to widen the window
// between "request found pending" and "request removed from the map" -
// before the fix that window spanned the callback's own execution, so any
// concurrent racer would still find the request present and invoke it too.
// With the fix, the request is popped from the map atomically before its
// callback runs, so a concurrent racer always finds it already gone.
func TestClient_RequestCallbackNotInvokedTwiceOnTimeoutRace(t *testing.T) {
	config := Config{ReadTimeout: time.Microsecond}
	c := NewJsonClient("ws://localhost:9000/connection/websocket", config)
	c.transport = noopTransport{}
	c.closeCh = make(chan struct{})

	const iterations = 20
	for i := 0; i < iterations; i++ {
		cmd := &protocol.Command{Id: uint32(i + 1)}
		var calls int32
		done := make(chan struct{}, 2)
		cb := func(reply *protocol.Reply, err error) {
			atomic.AddInt32(&calls, 1)
			time.Sleep(20 * time.Millisecond)
			done <- struct{}{}
		}

		if err := c.sendAsync(cmd, cb); err != nil {
			t.Fatalf("sendAsync: %v", err)
		}
		// Deliver the real reply concurrently with the sendAsync-spawned
		// ReadTimeout goroutine, which fires almost immediately given the
		// tiny ReadTimeout configured above.
		c.handle(nil, &protocol.Reply{Id: cmd.Id})

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("request %d: callback was never invoked", cmd.Id)
		}
		// Give the loser of the race a chance to (incorrectly) also fire.
		time.Sleep(30 * time.Millisecond)
		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Fatalf("request %d: callback invoked %d times, want 1", cmd.Id, got)
		}
	}
}
