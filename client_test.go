package centrifuge

import (
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

func TestConnectWrongAddress(t *testing.T) {
	client := New("ws://localhost:9000/connection/websocket", DefaultConfig())
	doneCh := make(chan error)
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
	case <-time.After(15 * time.Second):
		t.Errorf("expecting disconnect due to malformed address")
	}
}

func TestConnectJSON(t *testing.T) {
	client := New("ws://localhost:8000/connection/websocket?format=protobuf", DefaultConfig())
	doneCh := make(chan error)
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
	case <-time.After(15 * time.Second):
		t.Errorf("expecting successful connect")
	}
}
