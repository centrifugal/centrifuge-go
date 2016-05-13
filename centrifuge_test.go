package centrifuge

import (
	"encoding/json"
	"log"
	"testing"
	"time"
)

const (
	STATE_CONNECT = iota
	STATE_CONNECTED
	STATE_SUBSCRIBE
	STATE_SUBSCRIBED
	STATE_UNSUBSCRIBE
)

const (
	TestConnectionErrorMsg   = "test connection error"
	TestSubscriptionErrorMsg = "test subscription error"
)

var (
	states = map[string]int{
		"connect":     STATE_CONNECT,
		"subscribe":   STATE_SUBSCRIBE,
		"unsubscribe": STATE_UNSUBSCRIBE,
	}
)

type connectionMock struct {
	IsClosed         bool
	IncomingMessages []string

	currentIncoming int
	closed          chan struct{}
	reply           chan struct{}
	state           int

	uid    string
	method string

	errConn    bool
	errSub     bool
	errTimeout bool
}

func (c *connectionMock) initConnectionMock(url string, timeout time.Duration) (Conn, error) {
	c.closed = make(chan struct{})
	c.reply = make(chan struct{}, 64)
	return c, nil
}

func (c *connectionMock) Close() {
	c.IsClosed = true
	close(c.closed)
}

func (c *connectionMock) ReadMessage() (msg []byte, err error) {
	select {
	case <-c.reply:
		log.Printf("ReadMessage: %d", c.state) // need to reply
	case <-c.closed:
		return
	}

	switch c.state {
	case STATE_CONNECT:
		msg = c.getConnectAck()
		c.state = STATE_CONNECTED
	case STATE_SUBSCRIBE:
		errorString := ""
		if c.errSub {
			errorString = TestSubscriptionErrorMsg
		}
		body := subscribeResponseBody{
			Status: true,
		}
		msg = c.getAck(body, errorString)
		c.state = STATE_SUBSCRIBED
		c.reply <- struct{}{}
	case STATE_SUBSCRIBED:
		msg = c.getMessage()
	case STATE_UNSUBSCRIBE:
		body := unsubscribeResponseBody{
			Status: true,
		}
		msg = c.getAck(body, "")
		c.state = STATE_CONNECTED
	}
	return
}

func (c *connectionMock) WriteMessage(msg []byte) (err error) {
	if c.errTimeout {
		time.Sleep(DefaultTimeout + 1)
	}

	var cmd clientCommand
	err = json.Unmarshal(msg, &cmd)
	if err != nil {
		return
	}

	c.uid = cmd.UID
	c.method = cmd.Method

	state, ok := states[cmd.Method]
	if !ok {
		return
	}
	c.state = state

	c.reply <- struct{}{}

	return
}

func (c *connectionMock) Reset() {
	c.currentIncoming = 0
}

func (c *connectionMock) getMessage() (msg []byte) {
	l := len(c.IncomingMessages)
	if l == 0 || c.currentIncoming == l {
		// no test messages in queue
		return nil
	}

	if c.currentIncoming < l {
		c.reply <- struct{}{}
	}

	msg = []byte(c.IncomingMessages[c.currentIncoming])
	log.Printf("%s", string(msg))
	c.currentIncoming++
	return
}

func (c *connectionMock) getConnectAck() (ack []byte) {
	clientID := "cda49f81-44fb-4857-4ec8-ccae2670589a"
	body := &connectResponseBody{
		Client: clientID,
	}

	errorString := ""
	if c.errConn {
		errorString = TestConnectionErrorMsg
	}

	return c.getAck(&body, errorString)
}

func (c *connectionMock) getAck(body interface{}, errorString string) (ack []byte) {
	rawBody, _ := json.Marshal(body)
	resp := &response{
		UID:    c.uid,
		Method: c.method,
		Body:   json.RawMessage(rawBody),
		Error:  errorString,
	}
	ack, _ = json.Marshal(resp)
	return
}

func newTestCentrifuge(url string, creds *Credentials, events *EventHandler, config *Config, connMock connectionMock) *centrifuge {
	return &centrifuge{
		URL:         url,
		subs:        make(map[string]*sub),
		config:      config,
		credentials: creds,
		receive:     make(chan []byte, 64),
		write:       make(chan []byte, 64),
		closed:      make(chan struct{}),
		waiters:     make(map[string]chan response),
		events:      events,
		reconnect:   true,
		createConn:  connMock.initConnectionMock,
	}

}

var (
	url   = "ws://localhost/websocket"
	user  = "1"
	token = "4812af71acc1cd4c3db22fefb29b199db8eef11c775b43a67168d3aff8bdca5b"
	info  = ""
)

func testCredentials() *Credentials {
	return &Credentials{
		User:      user,
		Token:     token,
		Timestamp: Timestamp(),
		Info:      info,
	}
}

func TestConnectOk(t *testing.T) {
	c := newTestCentrifuge(url, testCredentials(), nil, DefaultConfig, connectionMock{})
	err := c.Connect()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}
}

func TestWriteTimeout(t *testing.T) {
	c := newTestCentrifuge(url, testCredentials(), nil, DefaultConfig, connectionMock{errTimeout: true})
	err := c.Connect()
	if err == nil {
		t.Error("Should fail")
	}
}

func TestConnectionError(t *testing.T) {
	c := newTestCentrifuge(url, testCredentials(), nil, DefaultConfig, connectionMock{errConn: true})
	err := c.Connect()
	if err == nil {
		t.Error("Should fail")
	}
	if err.Error() != TestConnectionErrorMsg {
		t.Errorf("Unexpected error message '%s'", err)
	}
}

func TestSubscriptionError(t *testing.T) {
	c := newTestCentrifuge(url, testCredentials(), nil, DefaultConfig, connectionMock{errSub: true})
	err := c.Connect()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}
	_, err = c.Subscribe("channel", nil)
	if err == nil {
		t.Error("Should fail")
	}
	if err.Error() != TestSubscriptionErrorMsg {
		t.Errorf("Unexpected error message '%s'", err)
	}
}

func TestUnexpectedMessageData(t *testing.T) {
	messages := []string{
		`MALFORMED_JSON "body":{"uid":"2118628f-6722-4492-6563-234a8dad003a","timestamp":"1461167202","info":null,"channel":"channel","data":{"ring":2,"count":136,"ids":[{"status":"0","id":"6bfc1544-60bf-4d48-9dd8-36f5d963f0a0","extended":{"extended_key":"extended_value"}}]},"client":""},"error":"","method":"message"`,
		`"body":{"uid":"2118628f-6722-4492-6563-234a8dad003a","timestamp":"1461167202","info":null,"channel":"channel","data":{"ring":2,"count":136,"ids":[{"status":"0","id":"6bfc1544-60bf-4d48-9dd8-36f5d963f0a0","extended":{"extended_key":"extended_value"}}]},"client":""},"error":"","method":"message"`,
	}

	positive := false
	for _, msg := range messages {
		jsonMessage(t, msg, positive)
	}
}

func TestInvalidMessageStructure(t *testing.T) {
	messages := []string{
		`{"invalid":{"uid":"2118628f-6722-4492-6563-234a8dad003a","timestamp":"1461167202","info":null,"channel":"channel","data":{"ring":2,"count":136,"ids":[{"status":"0","id":"6bfc1544-60bf-4d48-9dd8-36f5d963f0a0","extended":{"extended_key":"extended_value"}}]},"client":""},"error":"","method":"message}"`,
		`{"body":{"uuid_invalid":"2118628f-6722-4492-6563-234a8dad003a","timestamp":"1461167202","info":null,"channel":"channel","data":{"ring":2,"count":136,"ids":[{"status":"0","id":"6bfc1544-60bf-4d48-9dd8-36f5d963f0a0","extended":{"extended_key":"extended_value"}}]},"client":""},"error":"","method":"message}"`,
	}

	positive := false
	for _, msg := range messages {
		err := jsonMessage(t, msg, positive)
		if err == nil {
			t.Error("Should fail")
		}
	}
}

func TestMessageOk(t *testing.T) {
	messages := []string{
		`{"body":{"uid":"2118628f-6722-4492-6563-234a8dad003a","timestamp":"1461167202","info":null,"channel":"channel","data":{"ring":2,"count":136,"ids":[{"status":"0","id":"6bfc1544-60bf-4d48-9dd8-36f5d963f0a0","extended":{"extended_key":"extended_value"}}]},"client":""},"error":"","method":"message"}`,
	}

	positive := true
	for _, msg := range messages {
		err := jsonMessage(t, msg, positive)
		if err != nil {
			t.Errorf("Unexpected error message '%s'", err)
		}
	}
}

func TestNoPrivateSigner(t *testing.T) {
	c := newTestCentrifuge(url, testCredentials(), nil, DefaultConfig, connectionMock{})
	err := c.Connect()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}
	_, err = c.Subscribe("$private", nil)
	if err == nil {
		t.Error("Should fail")
	}
}

func TestPrivateSubscriptionOk(t *testing.T) {
	events := &EventHandler{
		OnPrivateSub: func(c Centrifuge, r *PrivateRequest) (*PrivateSign, error) {
			return &PrivateSign{Sign: "sign", Info: "info"}, nil
		},
	}

	c := newTestCentrifuge(url, testCredentials(), events, DefaultConfig, connectionMock{})
	err := c.Connect()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}

	_, err = c.Subscribe("$private", nil)
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}
}

func TestUnsubscribeOk(t *testing.T) {
	c := newTestCentrifuge(url, testCredentials(), nil, DefaultConfig, connectionMock{})
	err := c.Connect()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}

	sub, err := c.Subscribe("channel", nil)
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}

	if sub == nil {
		t.Error("Subscription is nil")
	}

	err = sub.Unsubscribe()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}
}

func jsonMessage(t *testing.T, msg string, positive bool) error {

	messages := []string{
		msg,
	}

	done := make(chan struct{})
	var what error
	events := &EventHandler{
		OnError: func(c Centrifuge, err error) {
			what = err
			done <- struct{}{}
		},
	}

	c := newTestCentrifuge(url, testCredentials(), events, DefaultConfig, connectionMock{IncomingMessages: messages})

	err := c.Connect()
	if err != nil {
		t.Errorf("Should pass but error is '%s'", err)
	}

	incoming := false
	subEvents := &SubEventHandler{
		OnMessage: func(sub Sub, msg Message) error {
			log.Printf("OnMessage: %v", msg)
			incoming = true
			done <- struct{}{}
			return nil
		},
	}

	_, err = c.Subscribe("channel", subEvents)
	if err != nil {
		t.Errorf("Unexpected error '%s'", err)
	}

	// wait for result
	tick := time.After(3 * time.Second)
	select {
	case <-tick:
		t.Errorf("waiting error timeout")
	case <-done: // wait
	}

	if positive {
		if !incoming {
			t.Error("No incoming message")
		}
	} else if what == nil {
		t.Error("Should fail")
	}

	return what
}
