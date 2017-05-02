package centrifuge

import (
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jpillora/backoff"
)

// Centrifuge represents connection to Centrifugo server.
type Centrifuge interface {
	// Connect allows to make connection to Centrifugo server.
	Connect() error
	// Reconnect allows to start reconnecting to Centrifugo.
	Reconnect(ReconnectStrategy) error
	// Subscribe allows to subscribe on channel and react on various subscription events.
	Subscribe(string, *SubEventHandler) (Sub, error)
	// ClientID returns client ID that Centrifugo gave to connection. Or empty string if no client ID issued yet.
	ClientID() string
	// Connected allows to check that client connected to Centrifugo at moment.
	Connected() bool
	// Subscribed allows to check that client subscribed on channel at moment.
	Subscribed(channel string) bool
	// Close closes connection to Centrifugo and clears all subscriptions.
	Close()
}

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
	ErrTimeout              = errors.New("timed out")
	ErrInvalidMessage       = errors.New("invalid message")
	ErrDuplicateWaiter      = errors.New("waiter with uid already exists")
	ErrWaiterClosed         = errors.New("waiter closed")
	ErrClientStatus         = errors.New("wrong client status to make operation")
	ErrClientDisconnected   = errors.New("client disconnected")
	ErrClientExpired        = errors.New("client expired")
	ErrReconnectForbidden   = errors.New("reconnect is not allowed after disconnect message")
	ErrReconnectFailed      = errors.New("reconnect failed")
	ErrBadSubscribeStatus   = errors.New("bad subscribe status")
	ErrBadUnsubscribeStatus = errors.New("bad unsubscribe status")
	ErrBadPublishStatus     = errors.New("bad publish status")
)

const (
	DefaultPrivateChannelPrefix = "$"
	DefaultTimeout              = 1 * time.Second
	DefaultReconnect            = true
)

// Config contains various client options.
type Config struct {
	Timeout              time.Duration
	PrivateChannelPrefix string
	Debug                bool
	Reconnect            bool
	SkipVerify           bool
}

// DefaultConfig with standard private channel prefix and 1 second timeout.
var DefaultConfig = &Config{
	PrivateChannelPrefix: DefaultPrivateChannelPrefix,
	Timeout:              DefaultTimeout,
	Reconnect:            DefaultReconnect,
}

// Private sign confirmes that client can subscribe on private channel.
type PrivateSign struct {
	Sign string
	Info string
}

// PrivateRequest contains info required to create PrivateSign when client
// wants to subscribe on private channel.
type PrivateRequest struct {
	ClientID string
	Channel  string
}

func newPrivateRequest(client string, channel string) *PrivateRequest {
	return &PrivateRequest{
		ClientID: client,
		Channel:  channel,
	}
}

// PrivateSubHandler needed to handle private channel subscriptions.
type PrivateSubHandler func(Centrifuge, *PrivateRequest) (*PrivateSign, error)

// RefreshHandler handles refresh event when client's credentials expired and must be refreshed.
type RefreshHandler func(Centrifuge) (*Credentials, error)

// DisconnectHandler is a function to handle disconnect event.
type DisconnectHandler func(Centrifuge) error

// ErrorHandler is a function to handle critical protocol errors manually.
type ErrorHandler func(Centrifuge, error)

// EventHandler contains callback functions that will be called when
// corresponding event happens with connection to Centrifuge.
type EventHandler struct {
	OnDisconnect DisconnectHandler
	OnPrivateSub PrivateSubHandler
	OnRefresh    RefreshHandler
	OnError      ErrorHandler
}

// Status shows actual connection status.
type Status int

const (
	DISCONNECTED = Status(iota)
	CONNECTED
	CLOSED
	RECONNECTING
)

// centrifuge describes client connection to Centrifugo server.
type centrifuge struct {
	mutex        sync.RWMutex
	URL          string
	config       *Config
	credentials  *Credentials
	conn         connection
	msgID        int32
	status       Status
	clientID     string
	subsMutex    sync.RWMutex
	subs         map[string]*sub
	waitersMutex sync.RWMutex
	waiters      map[string]chan response
	receive      chan []byte
	write        chan []byte
	closed       chan struct{}
	events       *EventHandler
	reconnect    bool
	createConn   connFactory
}

// MessageHandler is a function to handle messages in channels.
type MessageHandler func(Sub, Message) error

// JoinHandler is a function to handle join messages.
type JoinHandler func(Sub, ClientInfo) error

// LeaveHandler is a function to handle leave messages.
type LeaveHandler func(Sub, ClientInfo) error

// UnsubscribeHandler is a function to handle unsubscribe event.
type UnsubscribeHandler func(Sub) error

// SubEventHandler contains callback functions that will be called when
// corresponding event happens with subscription to channel.
type SubEventHandler struct {
	OnMessage     MessageHandler
	OnJoin        JoinHandler
	OnLeave       LeaveHandler
	OnUnsubscribe UnsubscribeHandler
}

// Sub respresents subscription on channel.
type Sub interface {
	// Channel allows to get channel this subscription belongs to.
	Channel() string
	// Publish allows to publish JSON data to channel.
	Publish(data []byte) error
	// History allows to get history messages for channel.
	History() ([]Message, error)
	// Presence allows to get presence info for channel.
	Presence() (map[string]ClientInfo, error)
	// Unsubscribe allows to unsubscribe from channel.
	Unsubscribe() error
}

type sub struct {
	channel       string
	centrifuge    *centrifuge
	events        *SubEventHandler
	lastMessageID *string
	lastMessageMu sync.RWMutex
}

func (c *centrifuge) newSub(channel string, events *SubEventHandler) *sub {
	return &sub{
		centrifuge: c,
		channel:    channel,
		events:     events,
	}
}

func (s *sub) Channel() string {
	return s.channel
}

// Publish JSON encoded data.
func (s *sub) Publish(data []byte) error {
	return s.centrifuge.publish(s.channel, data)
}

// History allows to extract channel history.
func (s *sub) History() ([]Message, error) {
	return s.centrifuge.history(s.channel)
}

// Presence allows to extract presence information for channel.
func (s *sub) Presence() (map[string]ClientInfo, error) {
	return s.centrifuge.presence(s.channel)
}

// Unsubscribe allows to unsubscribe from channel.
func (s *sub) Unsubscribe() error {
	return s.centrifuge.unsubscribe(s.channel)
}

func (s *sub) handleMessage(m Message) {
	var onMessage MessageHandler
	if s.events != nil && s.events.OnMessage != nil {
		onMessage = s.events.OnMessage
	}
	mid := m.UID
	s.lastMessageMu.Lock()
	s.lastMessageID = &mid
	s.lastMessageMu.Unlock()
	if onMessage != nil {
		onMessage(s, m)
	}
}

func (s *sub) handleJoinMessage(info ClientInfo) {
	var onJoin JoinHandler
	if s.events != nil && s.events.OnJoin != nil {
		onJoin = s.events.OnJoin
	}
	if onJoin != nil {
		onJoin(s, info)
	}
}

func (s *sub) handleLeaveMessage(info ClientInfo) {
	var onLeave LeaveHandler
	if s.events != nil && s.events.OnLeave != nil {
		onLeave = s.events.OnLeave
	}
	if onLeave != nil {
		onLeave(s, info)
	}
}

func (s *sub) resubscribe() error {
	privateSign, err := s.centrifuge.privateSign(s.channel)
	if err != nil {
		return err
	}
	s.lastMessageMu.Lock()
	msgID := *s.lastMessageID
	s.lastMessageMu.Unlock()
	body, err := s.centrifuge.sendSubscribe(s.channel, &msgID, privateSign)
	if err != nil {
		return err
	}
	if !body.Status {
		return ErrBadSubscribeStatus
	}

	if len(body.Messages) > 0 {
		for i := len(body.Messages) - 1; i >= 0; i-- {
			s.handleMessage(body.Messages[i])
		}
	} else {
		lastID := string(body.Last)
		s.lastMessageMu.Lock()
		s.lastMessageID = &lastID
		s.lastMessageMu.Unlock()
	}

	// resubscribe successfull.
	return nil
}

func (c *centrifuge) nextMsgID() int32 {
	return atomic.AddInt32(&c.msgID, 1)
}

// NewCenrifuge initializes Centrifuge struct. It accepts URL to Centrifugo server,
// connection Credentials, event handler and Config.
func NewCentrifuge(u string, creds *Credentials, events *EventHandler, config *Config) Centrifuge {
	c := &centrifuge{
		URL:         u,
		subs:        make(map[string]*sub),
		config:      config,
		credentials: creds,
		receive:     make(chan []byte, 64),
		write:       make(chan []byte, 64),
		closed:      make(chan struct{}),
		waiters:     make(map[string]chan response),
		events:      events,
		reconnect:   true,
		createConn:  newWSConnection,
	}
	return c
}

// Connected returns true if client is connected at moment.
func (c *centrifuge) Connected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.status == CONNECTED
}

// Subscribed returns true if client subscribed on channel.
func (c *centrifuge) Subscribed(channel string) bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.subscribed(channel)
}

func (c *centrifuge) subscribed(channel string) bool {
	c.subsMutex.RLock()
	_, ok := c.subs[channel]
	c.subsMutex.RUnlock()
	return ok
}

// ClientID returns client ID of this connection. It only available after connection
// was established and authorized.
func (c *centrifuge) ClientID() string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return string(c.clientID)
}

func (c *centrifuge) handleError(err error) {
	var onError ErrorHandler
	if c.events != nil && c.events.OnError != nil {
		onError = c.events.OnError
	}
	if onError != nil {
		onError(c, err)
	} else {
		log.Println(err)
		c.Close()
	}
}

// Close closes Centrifuge connection and clean ups everything.
func (c *centrifuge) Close() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.unsubscribeAll()
	c.close()
	c.status = CLOSED
}

// unsubscribeAll destroys all subscriptions.
// Instance Lock must be held outside.
func (c *centrifuge) unsubscribeAll() {
	if c.conn != nil && c.status == CONNECTED {
		for ch, sub := range c.subs {
			err := c.unsubscribe(sub.Channel())
			if err != nil {
				log.Println(err)
			}
			delete(c.subs, ch)
		}
	}
}

// close clean ups ws connection and all outgoing requests.
// Instance Lock must be held outside.
func (c *centrifuge) close() {
	if c.conn != nil {
		c.conn.Close()
	}

	c.waitersMutex.Lock()
	for uid, ch := range c.waiters {
		close(ch)
		delete(c.waiters, uid)
	}
	c.waitersMutex.Unlock()

	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
}

func (c *centrifuge) handleDisconnect(err error) {
	c.mutex.Lock()

	ok, _, reason := closeErr(err)
	if ok {
		var adv disconnectAdvice
		err := json.Unmarshal([]byte(reason), &adv)
		if err == nil {
			if !adv.Reconnect {
				c.reconnect = false
			}
		}
	}

	if c.status == CLOSED || c.status == RECONNECTING {
		c.mutex.Unlock()
		return
	}

	if c.conn != nil {
		c.conn.Close()
	}

	c.waitersMutex.Lock()
	for uid, ch := range c.waiters {
		close(ch)
		delete(c.waiters, uid)
	}
	c.waitersMutex.Unlock()

	select {
	case <-c.closed:
	default:
		close(c.closed)
	}

	c.status = DISCONNECTED

	var onDisconnect DisconnectHandler
	if c.events != nil && c.events.OnDisconnect != nil {
		onDisconnect = c.events.OnDisconnect
	}

	c.mutex.Unlock()

	if onDisconnect != nil {
		onDisconnect(c)
	}

}

type ReconnectStrategy interface {
	reconnect(c *centrifuge) error
}

type PeriodicReconnect struct {
	ReconnectInterval time.Duration
	NumReconnect      int
}

var DefaultPeriodicReconnect = &PeriodicReconnect{
	ReconnectInterval: 1 * time.Second,
	NumReconnect:      0,
}

func (r *PeriodicReconnect) reconnect(c *centrifuge) error {
	reconnects := 0
	for {
		if r.NumReconnect > 0 && reconnects >= r.NumReconnect {
			break
		}
		time.Sleep(r.ReconnectInterval)

		reconnects += 1

		err := c.doReconnect()
		if err != nil {
			log.Println(err)
			continue
		}

		// successfully reconnected
		return nil
	}
	return ErrReconnectFailed
}

type BackoffReconnect struct {
	// NumReconnect is maximum number of reconnect attempts, 0 means reconnect forever
	NumReconnect int
	//Factor is the multiplying factor for each increment step
	Factor float64
	//Jitter eases contention by randomizing backoff steps
	Jitter bool
	//Min and Max are the minimum and maximum values of the counter
	Min, Max time.Duration
}

var DefaultBackoffReconnect = &BackoffReconnect{
	NumReconnect: 0,
	Min:          100 * time.Millisecond,
	Max:          10 * time.Second,
	Factor:       2,
	Jitter:       true,
}

func (r *BackoffReconnect) reconnect(c *centrifuge) error {
	b := &backoff.Backoff{
		Min:    r.Min,
		Max:    r.Max,
		Factor: r.Factor,
		Jitter: r.Jitter,
	}
	reconnects := 0
	for {
		if r.NumReconnect > 0 && reconnects >= r.NumReconnect {
			break
		}
		time.Sleep(b.Duration())

		reconnects += 1

		err := c.doReconnect()
		if err != nil {
			log.Println(err)
			continue
		}

		// successfully reconnected
		return nil
	}
	return ErrReconnectFailed
}

func (c *centrifuge) doReconnect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.closed = make(chan struct{})

	err := c.connect()
	if err != nil {
		close(c.closed)
		return err
	}

	err = c.resubscribe()
	if err != nil {
		// we need just to close the connection and outgoing requests here
		// but preserve all subscriptions.
		c.close()
		return err
	}

	return nil
}

func (c *centrifuge) Reconnect(strategy ReconnectStrategy) error {
	c.mutex.Lock()
	reconnect := c.reconnect
	c.mutex.Unlock()
	if !reconnect {
		return ErrReconnectForbidden
	}
	c.mutex.Lock()
	c.status = RECONNECTING
	c.mutex.Unlock()
	return strategy.reconnect(c)
}

func (c *centrifuge) resubscribe() error {
	for _, sub := range c.subs {
		err := sub.resubscribe()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *centrifuge) read() {
	for {
		message, err := c.conn.ReadMessage()
		if err != nil {
			c.handleDisconnect(err)
			return
		}
		select {
		case <-c.closed:
			return
		default:
			c.receive <- message
		}
	}
}

func (c *centrifuge) run() {
	for {
		select {
		case msg := <-c.receive:
			err := c.handle(msg)
			if err != nil {
				c.handleError(err)
			}
		case msg := <-c.write:
			err := c.conn.WriteMessage(msg)
			if err != nil {
				c.handleError(err)
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
	default:
		return nil, ErrInvalidMessage
	}
	return resps, nil
}

func (c *centrifuge) handle(msg []byte) error {
	if len(msg) == 0 {
		return nil
	}
	resps, err := responsesFromClientMsg(msg)
	if err != nil {
		return err
	}
	for _, resp := range resps {
		if resp.UID != "" {
			c.waitersMutex.RLock()
			if waiter, ok := c.waiters[resp.UID]; ok {
				waiter <- resp
			}
			c.waitersMutex.RUnlock()
		} else {
			err := c.handleAsyncResponse(resp)
			if err != nil {
				c.handleError(err)
			}
		}
	}
	return nil
}

func (c *centrifuge) handleAsyncResponse(resp response) error {
	method := resp.Method
	errorStr := resp.Error
	body := resp.Body
	if errorStr != "" {
		// Should never occur in usual workflow.
		return errors.New(errorStr)
	}
	switch method {
	case "message":
		var m Message
		err := json.Unmarshal(body, &m)
		if err != nil {
			// Malformed message received.
			return errors.New("malformed message received from server")
		}
		channel := m.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			log.Println("message received but client not subscribed on channel")
			return nil
		}
		sub.handleMessage(m)
	case "join":
		var b joinLeaveMessage
		err := json.Unmarshal(body, &b)
		if err != nil {
			log.Println("malformed join message")
			return nil
		}
		channel := b.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			log.Printf("join received but client not subscribed on channel: %s", string(channel))
			return nil
		}
		sub.handleJoinMessage(b.Data)
	case "leave":
		var b joinLeaveMessage
		err := json.Unmarshal(body, &b)
		if err != nil {
			log.Println("malformed leave message")
			return nil
		}
		channel := b.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			log.Printf("leave received but client not subscribed on channel: %s", string(channel))
			return nil
		}
		sub.handleLeaveMessage(b.Data)
	default:
		return nil
	}
	return nil
}

// Lock must be held outside
func (c *centrifuge) connectWS() error {
	conn, err := c.createConn(c.URL, c.config.Timeout, c.config.SkipVerify)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

// Lock must be held outside
func (c *centrifuge) connect() error {
	err := c.connectWS()
	if err != nil {
		return err
	}

	go c.run()

	go c.read()

	var body connectResponseBody

	body, err = c.sendConnect()
	if err != nil {
		return err
	}

	if body.Expires && body.Expired {
		// Try to refresh credentials and repeat connection attempt.
		err = c.refreshCredentials()
		if err != nil {
			return err
		}
		body, err = c.sendConnect()
		if err != nil {
			return err
		}
		if body.Expires && body.Expired {
			return ErrClientExpired
		}
	}

	c.clientID = body.Client

	if body.Expires {
		go func(interval int64) {
			tick := time.After(time.Duration(interval) * time.Second)
			select {
			case <-c.closed:
				return
			case <-tick:
				err := c.sendRefresh()
				if err != nil {
					log.Println(err)
				}
			}
		}(body.TTL)
	}

	c.status = CONNECTED

	return nil
}

// Connect connects to Centrifugo and sends connect message to authorize.
func (c *centrifuge) Connect() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.status == CONNECTED {
		return ErrClientStatus
	}
	return c.connect()
}

func (c *centrifuge) refreshCredentials() error {
	var onRefresh RefreshHandler
	if c.events != nil && c.events.OnRefresh != nil {
		onRefresh = c.events.OnRefresh
	}
	if onRefresh == nil {
		return errors.New("RefreshHandler must be set to handle expired credentials")
	}

	creds, err := onRefresh(c)
	if err != nil {
		return err
	}
	c.credentials = creds
	return nil
}

func (c *centrifuge) sendRefresh() error {

	err := c.refreshCredentials()
	if err != nil {
		return err
	}

	cmd := refreshClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "refresh",
		},
		Params: refreshParams{
			User:      c.credentials.User,
			Timestamp: c.credentials.Timestamp,
			Info:      c.credentials.Info,
			Token:     c.credentials.Token,
		},
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return err
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	var body connectResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return err
	}
	if body.Expires {
		if body.Expired {
			return ErrClientExpired
		}
		go func(interval int64) {
			tick := time.After(time.Duration(interval) * time.Second)
			select {
			case <-c.closed:
				return
			case <-tick:
				err := c.sendRefresh()
				if err != nil {
					log.Println(err)
				}
			}
		}(body.TTL)
	}
	return nil
}

func (c *centrifuge) sendConnect() (connectResponseBody, error) {
	cmd := connectClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "connect",
		},
		Params: connectParams{
			User:      c.credentials.User,
			Timestamp: c.credentials.Timestamp,
			Info:      c.credentials.Info,
			Token:     c.credentials.Token,
		},
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return connectResponseBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return connectResponseBody{}, err
	}
	if r.Error != "" {
		return connectResponseBody{}, errors.New(r.Error)
	}
	var body connectResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return connectResponseBody{}, err
	}
	return body, nil
}

func (c *centrifuge) privateSign(channel string) (*PrivateSign, error) {
	var ps *PrivateSign
	var err error
	if strings.HasPrefix(channel, c.config.PrivateChannelPrefix) {
		if c.events != nil && c.events.OnPrivateSub != nil {
			privateReq := newPrivateRequest(c.ClientID(), channel)
			ps, err = c.events.OnPrivateSub(c, privateReq)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("PrivateSubHandler must be set to handle private channel subscriptions")
		}
	}
	return ps, nil
}

// Subscribe allows to subscribe on channel.
func (c *centrifuge) Subscribe(channel string, events *SubEventHandler) (Sub, error) {
	if !c.Connected() {
		return nil, ErrClientDisconnected
	}
	privateSign, err := c.privateSign(channel)
	if err != nil {
		return nil, err
	}
	c.subsMutex.Lock()
	sub := c.newSub(channel, events)
	c.subs[channel] = sub
	c.subsMutex.Unlock()

	sub.lastMessageMu.Lock()
	body, err := c.sendSubscribe(channel, sub.lastMessageID, privateSign)
	sub.lastMessageMu.Unlock()

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err != nil {
		c.subsMutex.Lock()
		delete(c.subs, channel)
		c.subsMutex.Unlock()
		return nil, err
	}
	if !body.Status {
		c.subsMutex.Lock()
		delete(c.subs, channel)
		c.subsMutex.Unlock()
		return nil, ErrBadSubscribeStatus
	}

	if len(body.Messages) > 0 {
		for i := len(body.Messages) - 1; i >= 0; i-- {
			sub.handleMessage(body.Messages[i])
		}
	} else {
		lastID := string(body.Last)
		sub.lastMessageMu.Lock()
		sub.lastMessageID = &lastID
		sub.lastMessageMu.Unlock()
	}

	// Subscription on channel successfull.
	return sub, nil
}

func (c *centrifuge) sendSubscribe(channel string, lastMessageID *string, privateSign *PrivateSign) (subscribeResponseBody, error) {

	params := subscribeParams{
		Channel: channel,
	}
	if lastMessageID != nil {
		params.Recover = true
		params.Last = *lastMessageID
	}
	if privateSign != nil {
		params.Client = c.ClientID()
		params.Info = privateSign.Info
		params.Sign = privateSign.Sign
	}

	cmd := subscribeClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "subscribe",
		},
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return subscribeResponseBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return subscribeResponseBody{}, err
	}
	if r.Error != "" {
		return subscribeResponseBody{}, errors.New(r.Error)
	}
	var body subscribeResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return subscribeResponseBody{}, err
	}
	return body, nil
}

func (c *centrifuge) publish(channel string, data []byte) error {
	body, err := c.sendPublish(channel, data)
	if err != nil {
		return err
	}
	if !body.Status {
		return ErrBadPublishStatus
	}
	return nil
}

func (c *centrifuge) sendPublish(channel string, data []byte) (publishResponseBody, error) {
	raw := json.RawMessage(data)
	params := publishParams{
		Channel: channel,
		Data:    &raw,
	}
	cmd := publishClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "publish",
		},
		Params: params,
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return publishResponseBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return publishResponseBody{}, err
	}
	if r.Error != "" {
		return publishResponseBody{}, errors.New(r.Error)
	}
	var body publishResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return publishResponseBody{}, err
	}
	return body, nil
}

func (c *centrifuge) history(channel string) ([]Message, error) {
	body, err := c.sendHistory(channel)
	if err != nil {
		return []Message{}, err
	}
	return body.Data, nil
}

func (c *centrifuge) sendHistory(channel string) (historyResponseBody, error) {
	cmd := historyClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "history",
		},
		Params: historyParams{
			Channel: channel,
		},
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return historyResponseBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return historyResponseBody{}, err
	}
	if r.Error != "" {
		return historyResponseBody{}, errors.New(r.Error)
	}
	var body historyResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return historyResponseBody{}, err
	}
	return body, nil
}

func (c *centrifuge) presence(channel string) (map[string]ClientInfo, error) {
	body, err := c.sendPresence(channel)
	if err != nil {
		return map[string]ClientInfo{}, err
	}
	return body.Data, nil
}

func (c *centrifuge) sendPresence(channel string) (presenceResponseBody, error) {
	cmd := presenceClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "presence",
		},
		Params: presenceParams{
			Channel: channel,
		},
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return presenceResponseBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return presenceResponseBody{}, err
	}
	if r.Error != "" {
		return presenceResponseBody{}, errors.New(r.Error)
	}
	var body presenceResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return presenceResponseBody{}, err
	}
	return body, nil
}

func (c *centrifuge) unsubscribe(channel string) error {
	if !c.subscribed(channel) {
		return nil
	}
	body, err := c.sendUnsubscribe(channel)
	if err != nil {
		return err
	}
	if !body.Status {
		return ErrBadUnsubscribeStatus
	}
	c.subsMutex.Lock()
	delete(c.subs, channel)
	c.subsMutex.Unlock()
	return nil
}

func (c *centrifuge) sendUnsubscribe(channel string) (unsubscribeResponseBody, error) {
	cmd := unsubscribeClientCommand{
		clientCommand: clientCommand{
			UID:    strconv.Itoa(int(c.nextMsgID())),
			Method: "unsubscribe",
		},
		Params: unsubscribeParams{
			Channel: channel,
		},
	}
	cmdBytes, err := json.Marshal(cmd)
	if err != nil {
		return unsubscribeResponseBody{}, err
	}
	r, err := c.sendSync(cmd.UID, cmdBytes)
	if err != nil {
		return unsubscribeResponseBody{}, err
	}
	if r.Error != "" {
		return unsubscribeResponseBody{}, errors.New(r.Error)
	}
	var body unsubscribeResponseBody
	err = json.Unmarshal(r.Body, &body)
	if err != nil {
		return unsubscribeResponseBody{}, err
	}
	return body, nil
}

func (c *centrifuge) sendSync(uid string, msg []byte) (response, error) {
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

func (c *centrifuge) send(msg []byte) error {
	select {
	case <-c.closed:
		return ErrClientDisconnected
	default:
		c.write <- msg
	}
	return nil
}

func (c *centrifuge) addWaiter(uid string, ch chan response) error {
	c.waitersMutex.Lock()
	defer c.waitersMutex.Unlock()
	if _, ok := c.waiters[uid]; ok {
		return ErrDuplicateWaiter
	}
	c.waiters[uid] = ch
	return nil
}

func (c *centrifuge) removeWaiter(uid string) error {
	c.waitersMutex.Lock()
	defer c.waitersMutex.Unlock()
	delete(c.waiters, uid)
	return nil
}

func (c *centrifuge) wait(ch chan response) (response, error) {
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
