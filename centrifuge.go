package centrifuge

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifugo/libcentrifugo"
	"github.com/gorilla/websocket"
)

// Timestamp is helper function to get current timestamp as string.
func Timestamp() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

// Credentials describe client connection parameters.
type Credentials struct {
	User      string
	Timestamp string
	Info      string
	Token     string
}

var (
	ErrTimeout            = errors.New("timed out")
	ErrWaiterClosed       = errors.New("waiter closed")
	ErrClientDisconnected = errors.New("client disconnected")
	ErrClientUnauthorized = errors.New("client not authorized")
)

// Config contains various client options.
type Config struct {
	Timeout              time.Duration
	PrivateChannelPrefix string
	RefreshEndpoint      string
	AuthEndpoint         string
	AuthHeaders          map[string]string
	RefreshHeaders       map[string]string
	Debug                bool
	Insecure             bool
}

var DefaultConfig = &Config{
	PrivateChannelPrefix: "$",
	Timeout:              5 * time.Second,
}

type clientCommand struct {
	UID    string      `json:"uid"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type response struct {
	UID    string          `json:"uid,omitempty"`
	Error  string          `json:"error"`
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body"`
}

// Centrifuge describes client connection to Centrifugo server.
type Centrifuge struct {
	sync.RWMutex
	URL         string
	config      *Config
	credentials *Credentials
	conn        *websocket.Conn
	msgID       int32
	connected   bool
	authorized  bool
	clientID    libcentrifugo.ConnID
	subs        map[string]*Subscription
	waiters     map[string]chan response
	receive     chan []byte
	write       chan []byte
	closed      chan struct{}
}

// MessageHandler is a function to handle messages in channels.
type MessageHandler func(libcentrifugo.Message) error

// JoinHandler is a function to handle join messages.
type JoinHandler func(libcentrifugo.ClientInfo) error

// LeaveHandler is a function to handle leave messages.
type LeaveHandler func(libcentrifugo.ClientInfo) error

// Subscription on channel.
type Subscription struct {
	centrifuge *Centrifuge
	Channel    string
	OnMessage  MessageHandler
	OnJoin     JoinHandler
	OnLeave    LeaveHandler
}

func newSubscription(c *Centrifuge, channel string) *Subscription {
	return &Subscription{
		centrifuge: c,
		Channel:    channel,
	}
}

// Publish JSON encoded data.
func (s *Subscription) Publish(data []byte) error {
	return s.centrifuge.publish(s.Channel, data)
}

// History allows to extract channel history.
func (s *Subscription) History() ([]libcentrifugo.Message, error) {
	return s.centrifuge.history(s.Channel)
}

// Presence allows to extract presence information for channel.
func (s *Subscription) Presence() (map[libcentrifugo.ConnID]libcentrifugo.ClientInfo, error) {
	return s.centrifuge.presence(s.Channel)
}

// Unsubscribe allows to unsubscribe from channel.
func (s *Subscription) Unsubscribe() error {
	return s.centrifuge.unsubscribe(s.Channel)
}

func (c *Centrifuge) nextMsgID() int32 {
	return atomic.AddInt32(&c.msgID, 1)
}

// NewCenrifuge initializes Centrifuge struct. It accepts URL to Centrifugo server,
// connection Credentials and Config.
func NewCentrifuge(u string, creds *Credentials, config *Config) *Centrifuge {
	c := &Centrifuge{
		URL:         u,
		subs:        make(map[string]*Subscription),
		config:      config,
		credentials: creds,
		receive:     make(chan []byte, 64),
		write:       make(chan []byte, 64),
		closed:      make(chan struct{}),
		waiters:     make(map[string]chan response),
	}
	go c.run()
	return c
}

// Connected returns true if client is connected at moment.
func (c *Centrifuge) Connected() bool {
	c.RLock()
	defer c.RUnlock()
	return c.connected
}

// Authorized returns true if client is authorized at moment.
func (c *Centrifuge) Authorized() bool {
	c.RLock()
	defer c.RUnlock()
	return c.authorized
}

// Subscribed returns true if client subscribed on channel.
func (c *Centrifuge) Subscribed(channel string) bool {
	c.RLock()
	defer c.RUnlock()
	_, ok := c.subs[channel]
	return ok
}

// ClientID returns client ID of this connection. It only available after connection
// was established and authorized.
func (c *Centrifuge) ClientID() string {
	c.RLock()
	defer c.RUnlock()
	return string(c.clientID)
}

// Close closes Centrifuge connection.
func (c *Centrifuge) Close() {
	c.Lock()
	if c.conn != nil {
		err := c.conn.Close()
		if err != nil {
			log.Println(err)
		}
	}
	c.connected = false
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	c.Unlock()
}

func (c *Centrifuge) read() {
	defer c.Close()
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
		select {
		case <-c.closed:
			return
		default:
			c.receive <- message
		}
	}
}

func (c *Centrifuge) run() {
	for {
		select {
		case msg := <-c.receive:
			err := c.handle(msg)
			if err != nil {
				log.Println(err)
				c.Close()
				return
			}
		case msg := <-c.write:
			c.conn.SetWriteDeadline(time.Now().Add(c.config.Timeout))
			err := c.conn.WriteMessage(websocket.TextMessage, msg)
			c.conn.SetWriteDeadline(time.Time{})
			if err != nil {
				log.Println(err)
				c.Close()
				return
			}
		case <-c.closed:
			return
		}
	}
}

var (
	arrayJsonPrefix  byte = '['
	objectJsonPrefix byte = '{'
)

func responsesFromClientMsg(msg []byte) ([]response, error) {
	var resps []response
	firstByte := msg[0]
	switch firstByte {
	case objectJsonPrefix:
		// single command request
		var resp response
		err := json.Unmarshal(msg, &resp)
		if err != nil {
			return nil, err
		}
		resps = append(resps, resp)
	case arrayJsonPrefix:
		// array of commands received
		err := json.Unmarshal(msg, &resps)
		if err != nil {
			return nil, err
		}
	}
	return resps, nil
}

func (c *Centrifuge) handle(msg []byte) error {
	resps, err := responsesFromClientMsg(msg)
	if err != nil {
		return err
	}
	for _, resp := range resps {
		if resp.UID != "" {
			c.RLock()
			if waiter, ok := c.waiters[resp.UID]; ok {
				waiter <- resp
			}
			c.RUnlock()
		} else {
			err := c.handleAsyncResponse(resp)
			if err != nil {
				log.Println(err)
				c.Close()
			}
		}
	}
	return nil
}

func (c *Centrifuge) handleAsyncResponse(resp response) error {
	method := resp.Method
	errorStr := resp.Error
	body := resp.Body
	if errorStr != "" {
		// Should never occur in usual workflow.
		return errors.New(errorStr)
	}
	switch method {
	case "message":
		var m libcentrifugo.Message
		err := json.Unmarshal(body, &m)
		if err != nil {
			// Malformed message received.
			return errors.New("malformed message received from server")
		}
		channel := m.Channel
		c.RLock()
		sub, ok := c.subs[string(channel)]
		if !ok {
			log.Println("message received but client not subscribed on channel")
			return nil
		}
		onMessage := sub.OnMessage
		c.RUnlock()
		if onMessage != nil {
			onMessage(m)
		}
	case "join":
		var b libcentrifugo.JoinLeaveBody
		err := json.Unmarshal(body, &b)
		if err != nil {
			log.Println("malformed join message")
			return nil
		}
		channel := b.Channel
		c.RLock()
		sub, ok := c.subs[string(channel)]
		if !ok {
			log.Println("join received but client not subscribed on channel")
			c.RUnlock()
			return nil
		}
		onJoin := sub.OnJoin
		c.RUnlock()
		if onJoin != nil {
			info := b.Data
			onJoin(info)
		}
	case "leave":
		var b libcentrifugo.JoinLeaveBody
		err := json.Unmarshal(body, &b)
		if err != nil {
			log.Println("malformed leave message")
			return nil
		}
		channel := b.Channel
		c.RLock()
		sub, ok := c.subs[string(channel)]
		if !ok {
			log.Println("leave received but client not subscribed on channel")
			c.RUnlock()
			return nil
		}
		onLeave := sub.OnLeave
		c.RUnlock()
		if onLeave != nil {
			info := b.Data
			onLeave(info)
		}
	default:
		return nil
	}
	return nil
}

// Connect connects to Centrifugo and sends connect message to authorize.
func (c *Centrifuge) Connect() error {
	c.Lock()
	if c.connected {
		return errors.New("client already connected")
	}
	wsHeaders := http.Header{}
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(c.URL, wsHeaders)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return errors.New("Wrong status code while connecting to server")
	}
	c.connected = true
	c.conn = conn
	c.Unlock()

	go c.read()

	body, err := c.sendConnect()
	if err != nil {
		return err
	}
	c.Lock()
	c.clientID = body.Client
	// TODO: expired check and TTL support.
	c.authorized = true
	c.Unlock()
	return nil
}

func (c *Centrifuge) sendConnect() (libcentrifugo.ConnectBody, error) {
	params := c.connectParams()
	cmd := clientCommand{
		UID:    strconv.Itoa(int(c.nextMsgID())),
		Method: "connect",
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return libcentrifugo.ConnectBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return libcentrifugo.ConnectBody{}, err
	}
	if r.Error != "" {
		return libcentrifugo.ConnectBody{}, errors.New(r.Error)
	}
	var body libcentrifugo.ConnectBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return libcentrifugo.ConnectBody{}, err
	}
	return body, nil
}

func (c *Centrifuge) connectParams() *libcentrifugo.ConnectClientCommand {
	return &libcentrifugo.ConnectClientCommand{
		User:      libcentrifugo.UserID(c.credentials.User),
		Timestamp: c.credentials.Timestamp,
		Info:      c.credentials.Info,
		Token:     c.credentials.Token,
	}
}

// Subscribe allows to subscribe on channel.
func (c *Centrifuge) Subscribe(channel string) (*Subscription, error) {
	if !c.Authorized() {
		return nil, ErrClientUnauthorized
	}
	body, err := c.sendSubscribe(channel)
	if err != nil {
		return nil, err
	}
	if !body.Status {
		return nil, errors.New("wrong subscribe status")
	}
	// Subscription successfull.
	sub := newSubscription(c, channel)

	c.Lock()
	c.subs[channel] = sub
	c.Unlock()

	return sub, nil
}

func (c *Centrifuge) subscribeParams(channel string) *libcentrifugo.SubscribeClientCommand {
	return &libcentrifugo.SubscribeClientCommand{
		Channel: libcentrifugo.Channel(channel),
	}
}

func (c *Centrifuge) sendSubscribe(channel string) (libcentrifugo.SubscribeBody, error) {
	params := c.subscribeParams(channel)
	cmd := clientCommand{
		UID:    strconv.Itoa(int(c.nextMsgID())),
		Method: "subscribe",
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return libcentrifugo.SubscribeBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return libcentrifugo.SubscribeBody{}, err
	}
	if r.Error != "" {
		return libcentrifugo.SubscribeBody{}, errors.New(r.Error)
	}
	var body libcentrifugo.SubscribeBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return libcentrifugo.SubscribeBody{}, err
	}
	return body, nil
}

func (c *Centrifuge) publish(channel string, data []byte) error {
	if !c.Authorized() {
		return ErrClientUnauthorized
	}
	body, err := c.sendPublish(channel, data)
	if err != nil {
		return err
	}
	if !body.Status {
		return errors.New("wrong publish status")
	}
	return nil
}

func (c *Centrifuge) publishParams(channel string, data []byte) *libcentrifugo.PublishClientCommand {
	return &libcentrifugo.PublishClientCommand{
		Channel: libcentrifugo.Channel(channel),
		Data:    json.RawMessage(data),
	}
}

func (c *Centrifuge) sendPublish(channel string, data []byte) (libcentrifugo.PublishBody, error) {
	params := c.publishParams(channel, data)
	cmd := clientCommand{
		UID:    strconv.Itoa(int(c.nextMsgID())),
		Method: "publish",
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return libcentrifugo.PublishBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return libcentrifugo.PublishBody{}, err
	}
	if r.Error != "" {
		return libcentrifugo.PublishBody{}, errors.New(r.Error)
	}
	var body libcentrifugo.PublishBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return libcentrifugo.PublishBody{}, err
	}
	return body, nil
}

func (c *Centrifuge) history(channel string) ([]libcentrifugo.Message, error) {
	if !c.Authorized() {
		return nil, ErrClientUnauthorized
	}
	body, err := c.sendHistory(channel)
	if err != nil {
		return []libcentrifugo.Message{}, err
	}
	return body.Data, nil
}

func (c *Centrifuge) historyParams(channel string) *libcentrifugo.HistoryClientCommand {
	return &libcentrifugo.HistoryClientCommand{
		Channel: libcentrifugo.Channel(channel),
	}
}

func (c *Centrifuge) sendHistory(channel string) (libcentrifugo.HistoryBody, error) {
	params := c.historyParams(channel)
	cmd := clientCommand{
		UID:    strconv.Itoa(int(c.nextMsgID())),
		Method: "history",
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return libcentrifugo.HistoryBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return libcentrifugo.HistoryBody{}, err
	}
	if r.Error != "" {
		return libcentrifugo.HistoryBody{}, errors.New(r.Error)
	}
	var body libcentrifugo.HistoryBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return libcentrifugo.HistoryBody{}, err
	}
	return body, nil
}

func (c *Centrifuge) presence(channel string) (map[libcentrifugo.ConnID]libcentrifugo.ClientInfo, error) {
	if !c.Authorized() {
		return nil, ErrClientUnauthorized
	}
	body, err := c.sendPresence(channel)
	if err != nil {
		return map[libcentrifugo.ConnID]libcentrifugo.ClientInfo{}, err
	}
	return body.Data, nil
}

func (c *Centrifuge) presenceParams(channel string) *libcentrifugo.PresenceClientCommand {
	return &libcentrifugo.PresenceClientCommand{
		Channel: libcentrifugo.Channel(channel),
	}
}

func (c *Centrifuge) sendPresence(channel string) (libcentrifugo.PresenceBody, error) {
	params := c.presenceParams(channel)
	cmd := clientCommand{
		UID:    strconv.Itoa(int(c.nextMsgID())),
		Method: "presence",
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return libcentrifugo.PresenceBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return libcentrifugo.PresenceBody{}, err
	}
	if r.Error != "" {
		return libcentrifugo.PresenceBody{}, errors.New(r.Error)
	}
	var body libcentrifugo.PresenceBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return libcentrifugo.PresenceBody{}, err
	}
	return body, nil
}

func (c *Centrifuge) unsubscribe(channel string) error {
	if !c.Authorized() {
		return ErrClientUnauthorized
	}
	if c.Subscribed(channel) {
		return nil
	}
	body, err := c.sendUnsubscribe(channel)
	if err != nil {
		return err
	}
	if !body.Status {
		return errors.New("wrong unsubscribe status")
	}
	c.Lock()
	delete(c.subs, channel)
	c.Unlock()
	return nil
}

func (c *Centrifuge) unsubscribeParams(channel string) *libcentrifugo.UnsubscribeClientCommand {
	return &libcentrifugo.UnsubscribeClientCommand{
		Channel: libcentrifugo.Channel(channel),
	}
}

func (c *Centrifuge) sendUnsubscribe(channel string) (libcentrifugo.UnsubscribeBody, error) {
	params := c.unsubscribeParams(channel)
	cmd := clientCommand{
		UID:    strconv.Itoa(int(c.nextMsgID())),
		Method: "unsubscribe",
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return libcentrifugo.UnsubscribeBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return libcentrifugo.UnsubscribeBody{}, err
	}
	if r.Error != "" {
		return libcentrifugo.UnsubscribeBody{}, errors.New(r.Error)
	}
	var body libcentrifugo.UnsubscribeBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return libcentrifugo.UnsubscribeBody{}, err
	}
	return body, nil
}

func (c *Centrifuge) sendSync(uid string, msg []byte) (response, error) {
	wait := make(chan response)
	err := c.addWaiter(uid, wait)
	defer c.removeWaiter(uid)
	if err != nil {
		return response{}, err
	}
	err = c.send(msg)
	if err != nil {
		return response{}, err
	}
	return c.wait(wait)
}

func (c *Centrifuge) send(msg []byte) error {
	if !c.Connected() {
		return ErrClientDisconnected
	}
	select {
	case <-c.closed:
		return ErrClientDisconnected
	default:
		c.write <- msg
	}
	return nil
}

func (c *Centrifuge) addWaiter(uid string, ch chan response) error {
	c.Lock()
	defer c.Unlock()
	if _, ok := c.waiters[uid]; ok {
		return errors.New("Waiter with uid already exists")
	}
	c.waiters[uid] = ch
	return nil
}

func (c *Centrifuge) removeWaiter(uid string) error {
	c.Lock()
	defer c.Unlock()
	delete(c.waiters, uid)
	return nil
}

func (c *Centrifuge) wait(ch chan response) (response, error) {
	select {
	case data, ok := <-ch:
		if !ok {
			return response{}, ErrWaiterClosed
		}
		return data, nil
	case <-time.After(c.config.Timeout):
		return response{}, ErrTimeout
	case <-c.closed:
		return response{}, ErrClientDisconnected
	}
}
