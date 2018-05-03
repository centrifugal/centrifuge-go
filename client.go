package centrifuge

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge-go/internal/proto"
	"github.com/jpillora/backoff"
)

// Credentials describe client connection parameters used for authentication.
type Credentials struct {
	// User is current user ID.
	User string
	// Exp is timestamp seconds connection must be kept open. Connection can
	// be refreshed and prolonged using Refresh mechanism.
	Exp string
	// Optional base64 encoded connection information. It must be valid JSON
	// encoded into UTF-8 and then transformed to base64 in case of JSON protocol
	// and any bytes transformed to base64 in case of Protobuf protocol.
	Info string
	// Sign is HMAC SHA-256 sign based on credentials above and shared (between
	// Centrifuge server and application backend) secret key.
	Sign string
}

// NewCredentials initializes Credentials.
func NewCredentials(user, exp, info, sign string) *Credentials {
	return &Credentials{
		User: user,
		Exp:  exp,
		Info: info,
		Sign: sign,
	}
}

type disconnect struct {
	Reason    string
	Reconnect bool
}

var (
	// ErrTimeout ...
	ErrTimeout = errors.New("timeout")
	// ErrClientClosed ...
	ErrClientClosed = errors.New("client closed")
	// ErrClientDisconnected ...
	ErrClientDisconnected = errors.New("client disconnected")
	// ErrClientExpired ...
	ErrClientExpired = errors.New("client connection expired")
	// ErrReconnectFailed ...
	ErrReconnectFailed = errors.New("reconnect failed")
)

const (
	// DefaultReadTimeout ...
	DefaultReadTimeout = 5 * time.Second
	// DefaultWriteTimeout ...
	DefaultWriteTimeout = 5 * time.Second
	// DefaultPingInterval ...
	DefaultPingInterval = 25 * time.Second
	// DefaultPrivateChannelPrefix ...
	DefaultPrivateChannelPrefix = "$"
)

// Config contains various client options.
type Config struct {
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	PingInterval         time.Duration
	PrivateChannelPrefix string
	Websocket            WebsocketConfig
	GRPC                 GRPCConfig
}

// DefaultConfig returns Config with default options.
func DefaultConfig() Config {
	return Config{
		PingInterval:         DefaultPingInterval,
		ReadTimeout:          DefaultReadTimeout,
		WriteTimeout:         DefaultWriteTimeout,
		PrivateChannelPrefix: DefaultPrivateChannelPrefix,
		Websocket:            WebsocketConfig{},
		GRPC:                 GRPCConfig{},
	}
}

// PrivateSign confirmes that client can subscribe on private channel.
type PrivateSign struct {
	Sign string
	Info string
}

// PrivateSubEvent contains info required to create PrivateSign when client
// wants to subscribe on private channel.
type PrivateSubEvent struct {
	ClientID string
	Channel  string
}

// ConnectEvent is a connect event context passed to OnConnect callback.
type ConnectEvent struct {
	ClientID string
	Version  string
	Data     []byte
}

// DisconnectEvent is a disconnect event context passed to OnDisconnect callback.
type DisconnectEvent struct {
	Reason    string
	Reconnect bool
}

// ErrorEvent is an error event context passed to OnError callback.
type ErrorEvent struct {
	Message string
}

// MessageEvent is an event for async message from server to client.
type MessageEvent struct {
	Data []byte
}

// ConnectHandler is an interface describing how to handle connect event.
type ConnectHandler interface {
	OnConnect(*Client, ConnectEvent)
}

// DisconnectHandler is an interface describing how to handle disconnect event.
type DisconnectHandler interface {
	OnDisconnect(*Client, DisconnectEvent)
}

// MessageHandler is an interface describing how to async message from server.
type MessageHandler interface {
	OnMessage(*Client, MessageEvent)
}

// PrivateSubHandler is an interface describing how to handle private subscription request.
type PrivateSubHandler interface {
	OnPrivateSub(*Client, PrivateSubEvent) (PrivateSign, error)
}

// RefreshHandler is an interface describing how to handle credentials refresh event.
type RefreshHandler interface {
	OnRefresh(*Client) (Credentials, error)
}

// ErrorHandler is an interface describing how to handle error event.
type ErrorHandler interface {
	OnError(*Client, ErrorEvent)
}

// EventHub has all event handlers for client.
type EventHub struct {
	onConnect    ConnectHandler
	onDisconnect DisconnectHandler
	onPrivateSub PrivateSubHandler
	onRefresh    RefreshHandler
	onError      ErrorHandler
	onMessage    MessageHandler
}

// NewEventHub initializes new EventHub.
func NewEventHub() *EventHub {
	return &EventHub{}
}

// OnConnect is a function to handle connect event.
func (h *EventHub) OnConnect(handler ConnectHandler) {
	h.onConnect = handler
}

// OnDisconnect is a function to handle disconnect event.
func (h *EventHub) OnDisconnect(handler DisconnectHandler) {
	h.onDisconnect = handler
}

// OnPrivateSub needed to handle private channel subscriptions.
func (h *EventHub) OnPrivateSub(handler PrivateSubHandler) {
	h.onPrivateSub = handler
}

// OnRefresh handles refresh event when client's credentials expired and must be refreshed.
func (h *EventHub) OnRefresh(handler RefreshHandler) {
	h.onRefresh = handler
}

// OnError is a function that will receive unhandled errors for logging.
func (h *EventHub) OnError(handler ErrorHandler) {
	h.onError = handler
}

// OnMessage allows to process async message from server to client.
func (h *EventHub) OnMessage(handler MessageHandler) {
	h.onMessage = handler
}

// Describe client connection statuses.
const (
	DISCONNECTED = iota
	CONNECTING
	CONNECTED
	CLOSED
)

// Client describes client connection to Centrifugo server.
type Client struct {
	mutex             sync.RWMutex
	url               string
	encoding          proto.Encoding
	config            Config
	credentials       *Credentials
	connectData       proto.Raw
	transport         transport
	msgID             int32
	status            int
	id                string
	subsMutex         sync.RWMutex
	subs              map[string]*Subscription
	requestsMutex     sync.RWMutex
	requests          map[uint32]chan proto.Reply
	receive           chan []byte
	write             chan *proto.Command
	closeCh           chan struct{}
	reconnect         bool
	reconnectStrategy reconnectStrategy
	events            *EventHub
	delayPing         chan struct{}
	paramsEncoder     proto.ParamsEncoder
	resultDecoder     proto.ResultDecoder
	commandEncoder    proto.CommandEncoder
	pushEncoder       proto.PushEncoder
	pushDecoder       proto.PushDecoder
}

func (c *Client) nextMsgID() int32 {
	return atomic.AddInt32(&c.msgID, 1)
}

// New initializes Client.
func New(u string, events *EventHub, config Config) *Client {
	var encoding proto.Encoding

	if strings.HasPrefix(u, "ws") {
		if strings.Contains(strings.ToLower(u), "format=protobuf") {
			encoding = proto.EncodingProtobuf
		} else {
			encoding = proto.EncodingJSON
		}
	} else if strings.HasPrefix(u, "grpc") {
		encoding = proto.EncodingProtobuf
	} else {
		panic(fmt.Sprintf("unsupported connection endpoint: %s", u))
	}

	c := &Client{
		url:               u,
		encoding:          encoding,
		subs:              make(map[string]*Subscription),
		config:            config,
		requests:          make(map[uint32]chan proto.Reply),
		reconnect:         true,
		reconnectStrategy: defaultBackoffReconnect,
		events:            events,
		delayPing:         make(chan struct{}, 32),
		paramsEncoder:     proto.NewParamsEncoder(encoding),
		resultDecoder:     proto.NewResultDecoder(encoding),
		commandEncoder:    proto.NewCommandEncoder(encoding),
		pushEncoder:       proto.NewPushEncoder(encoding),
		pushDecoder:       proto.NewPushDecoder(encoding),
	}
	return c
}

// SetCredentials allows to set credentials to let client
// authenticate itself on connect.
func (c *Client) SetCredentials(creds *Credentials) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.credentials = creds
}

// SetConnectData allows to set data to send in connect message.
func (c *Client) SetConnectData(data proto.Raw) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.connectData = data
}

func (c *Client) subscribed(channel string) bool {
	c.subsMutex.RLock()
	_, ok := c.subs[channel]
	c.subsMutex.RUnlock()
	return ok
}

// clientID returns client ID of this connection. It only available after
// connection was established and authorized.
func (c *Client) clientID() string {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.id
}

func (c *Client) handleError(err error) {
	var handler ErrorHandler
	if c.events != nil && c.events.onError != nil {
		handler = c.events.onError
	}
	if handler != nil {
		handler.OnError(c, ErrorEvent{Message: err.Error()})
	}
}

// Send data to server asynchronously.
func (c *Client) Send(data []byte) error {
	cmd := &proto.Command{
		Method: proto.MethodTypeSend,
	}
	params := &proto.SendRequest{
		Data: data,
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return err
	}
	cmd.Params = paramsData
	return c.send(cmd)
}

// RPC allows to make RPC – send data to server ant wait for response.
// RPC handler must be registered on server.
func (c *Client) RPC(data []byte) ([]byte, error) {
	cmd := &proto.Command{
		Method: proto.MethodTypeRPC,
	}
	params := proto.RPCRequest{
		Data: data,
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return nil, err
	}
	cmd.Params = paramsData
	r, err := c.sendSync(cmd)
	if err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, r.Error
	}

	var res proto.RPCResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return nil, err
	}

	return res.Data, nil
}

// Close closes Client connection and cleans ups everything.
func (c *Client) Close() error {
	err := c.Disconnect()
	c.mutex.Lock()
	c.status = CLOSED
	c.mutex.Unlock()
	return err
}

// close clean ups ws connection and all outgoing requests.
// Instance Lock must be held outside.
func (c *Client) close() {
	c.requestsMutex.Lock()
	for uid, ch := range c.requests {
		close(ch)
		delete(c.requests, uid)
	}
	c.requestsMutex.Unlock()

	if c.transport != nil {
		c.transport.Close()
		c.transport = nil
	}
}

func (c *Client) handleDisconnect(d *disconnect) {
	if d == nil {
		d = &disconnect{
			Reason:    "connection closed",
			Reconnect: true,
		}
	}

	c.mutex.Lock()
	if c.status == DISCONNECTED || c.status == CLOSED {
		c.mutex.Unlock()
		return
	}

	c.reconnect = d.Reconnect

	if c.transport != nil {
		c.transport.Close()
		c.transport = nil
	}

	c.requestsMutex.Lock()
	for uid, ch := range c.requests {
		close(ch)
		delete(c.requests, uid)
	}
	c.requestsMutex.Unlock()

	select {
	case <-c.closeCh:
	default:
		close(c.closeCh)
	}
	c.status = DISCONNECTED

	c.subsMutex.RLock()
	unsubs := make([]*Subscription, 0, len(c.subs))
	for _, s := range c.subs {
		unsubs = append(unsubs, s)
	}
	c.subsMutex.RUnlock()

	for _, s := range unsubs {
		s.triggerOnUnsubscribe(true)
	}

	reconnect := c.reconnect
	c.mutex.Unlock()

	var handler DisconnectHandler
	if c.events != nil && c.events.onDisconnect != nil {
		handler = c.events.onDisconnect
	}

	if handler != nil {
		handler.OnDisconnect(c, DisconnectEvent{Reason: d.Reason, Reconnect: reconnect})
	}

	if !reconnect {
		return
	}

	err := c.reconnectStrategy.reconnect(c)
	if err != nil {
		c.Close()
	}
}

type reconnectStrategy interface {
	reconnect(c *Client) error
}

type backoffReconnect struct {
	// NumReconnect is maximum number of reconnect attempts, 0 means reconnect forever.
	NumReconnect int
	// Factor is the multiplying factor for each increment step.
	Factor float64
	// Jitter eases contention by randomizing backoff steps.
	Jitter bool
	// MinMilliseconds is a minimum value of the reconnect interval.
	MinMilliseconds int
	// MaxMilliseconds is a maximum value of the reconnect interval.
	MaxMilliseconds int
}

var defaultBackoffReconnect = &backoffReconnect{
	NumReconnect:    0,
	MinMilliseconds: 100,
	MaxMilliseconds: 10 * 1000,
	Factor:          2,
	Jitter:          true,
}

func (r *backoffReconnect) reconnect(c *Client) error {
	b := &backoff.Backoff{
		Min:    time.Duration(r.MinMilliseconds) * time.Millisecond,
		Max:    time.Duration(r.MaxMilliseconds) * time.Millisecond,
		Factor: r.Factor,
		Jitter: r.Jitter,
	}
	reconnects := 0

	for {
		if r.NumReconnect > 0 && reconnects >= r.NumReconnect {
			break
		}
		time.Sleep(b.Duration())

		reconnects++
		err := c.doReconnect()
		if err != nil {
			continue
		}

		// successfully reconnected
		return nil
	}
	return ErrReconnectFailed
}

func (c *Client) doReconnect() error {
	err := c.connect()
	if err != nil {
		c.close()
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

func (c *Client) pinger(closeCh chan struct{}) {
	timeout := time.Duration(c.config.PingInterval)
	for {
		select {
		case <-c.delayPing:
		case <-time.After(timeout):
			err := c.sendPing()
			if err != nil {
				c.handleDisconnect(&disconnect{Reason: "no ping", Reconnect: true})
				return
			}
		case <-closeCh:
			return
		}
	}
}

func (c *Client) reader(t transport, closeCh chan struct{}) {
	for {
		reply, disconnect, err := t.Read()
		if err != nil {
			c.handleDisconnect(disconnect)
			return
		}
		select {
		case <-closeCh:
			return
		default:
			select {
			case c.delayPing <- struct{}{}:
			default:
			}
			err := c.handle(reply)
			if err != nil {
				c.handleError(err)
			}
		}
	}
}

func (c *Client) writer(t transport, closeCh chan struct{}) {
	for {
		select {
		case cmd := <-c.write:
			err := t.Write(cmd, c.config.WriteTimeout)
			if err != nil {
				c.handleDisconnect(&disconnect{Reason: "write error", Reconnect: true})
				return
			}
		case <-closeCh:
			return
		}
	}
}

func (c *Client) handle(reply *proto.Reply) error {
	if reply.ID > 0 {
		c.requestsMutex.RLock()
		if waiter, ok := c.requests[reply.ID]; ok {
			waiter <- *reply
		}
		c.requestsMutex.RUnlock()
	} else {
		push, err := c.pushDecoder.Decode(reply.Result)
		if err != nil {
			c.handleError(err)
			return err
		}
		err = c.handlePush(*push)
		if err != nil {
			c.handleError(err)
		}
	}

	return nil
}

func (c *Client) handleMessage(msg proto.Message) error {

	var handler MessageHandler
	if c.events != nil && c.events.onMessage != nil {
		handler = c.events.onMessage
	}

	if handler != nil {
		ctx := MessageEvent{Data: msg.Data}
		handler.OnMessage(c, ctx)
	}

	return nil
}

func (c *Client) handlePush(msg proto.Push) error {
	switch msg.Type {
	case proto.PushTypeMessage:
		m, err := c.pushDecoder.DecodeMessage(msg.Data)
		if err != nil {
			return err
		}
		c.handleMessage(*m)
	case proto.PushTypeUnsub:
		m, err := c.pushDecoder.DecodeUnsub(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			return nil
		}
		sub.handleUnsub(*m)
	case proto.PushTypePublication:
		m, err := c.pushDecoder.DecodePublication(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			return nil
		}
		sub.handlePublication(*m)
	case proto.PushTypeJoin:
		m, err := c.pushDecoder.DecodeJoin(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			return nil
		}
		sub.handleJoin(m.Info)
	case proto.PushTypeLeave:
		m, err := c.pushDecoder.DecodeLeave(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[string(channel)]
		c.subsMutex.RUnlock()
		if !ok {
			return nil
		}
		sub.handleLeave(m.Info)
	default:
		return nil
	}
	return nil
}

// Connect dials to server and sends connect message.
func (c *Client) Connect() error {
	c.mutex.Lock()
	if c.status == CONNECTED || c.status == CONNECTING {
		c.mutex.Unlock()
		return nil
	}
	if c.status == CLOSED {
		c.mutex.Unlock()
		return ErrClientClosed
	}
	c.status = CONNECTING
	c.reconnect = true
	c.mutex.Unlock()

	err := c.connect()
	if err != nil {
		if c.transport == nil {
			c.handleError(err)
			c.handleDisconnect(nil)
		}
		return nil
	}
	err = c.resubscribe()
	if err != nil {
		// we need just to close the connection and outgoing requests here
		// but preserve all subscriptions.
		c.close()
		return nil
	}

	return nil
}

func (c *Client) connect() error {
	c.mutex.Lock()
	if c.status == CONNECTED {
		c.mutex.Unlock()
		return nil
	}
	c.status = CONNECTING
	c.closeCh = make(chan struct{})
	c.mutex.Unlock()

	var t transport
	var err error

	if strings.HasPrefix(c.url, "ws") {
		t, err = newWebsocketTransport(c.url, c.encoding, c.config.Websocket)
		if err != nil {
			return err
		}
	} else {
		t, err = newGRPCTransport(c.url, c.config.GRPC)
		if err != nil {
			return err
		}
	}

	c.mutex.Lock()
	if c.status == DISCONNECTED {
		c.mutex.Unlock()
		return nil
	}

	c.transport = t
	closeCh := make(chan struct{})
	c.closeCh = closeCh
	c.write = make(chan *proto.Command, 64)
	c.receive = make(chan []byte, 64)
	c.mutex.Unlock()

	go c.reader(t, closeCh)
	go c.writer(t, closeCh)

	var res proto.ConnectResult

	res, err = c.sendConnect()
	if err != nil {
		return err
	}

	if res.Expires && res.Expired {
		// Try to refresh credentials and repeat connection attempt.
		err = c.refreshCredentials()
		if err != nil {
			c.Close()
			return err
		}
		res, err = c.sendConnect()
		if err != nil {
			c.Close()
			return err
		}
		if res.Expires && res.Expired {
			c.Close()
			return ErrClientExpired
		}
	}

	c.mutex.Lock()
	c.id = res.Client
	prevStatus := c.status
	c.status = CONNECTED
	c.mutex.Unlock()

	if res.Expires {
		go func(interval uint32) {
			tick := time.After(time.Duration(interval) * time.Second)
			select {
			case <-c.closeCh:
				return
			case <-tick:
				c.sendRefresh()
			}
		}(res.TTL)
	}

	go c.pinger(closeCh)

	if c.events != nil && c.events.onConnect != nil && prevStatus != CONNECTED {
		handler := c.events.onConnect
		ev := ConnectEvent{
			ClientID: c.clientID(),
			Version:  res.Version,
			Data:     res.Data,
		}
		handler.OnConnect(c, ev)
	}

	return nil
}

func (c *Client) resubscribe() error {
	c.subsMutex.RLock()
	defer c.subsMutex.RUnlock()
	for _, sub := range c.subs {
		err := sub.resubscribe()
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) disconnect(reconnect bool) error {
	c.mutex.Lock()
	c.reconnect = reconnect
	c.mutex.Unlock()
	c.handleDisconnect(&disconnect{
		Reconnect: reconnect,
		Reason:    "clean disconnect",
	})
	return nil
}

// Disconnect client from server.
func (c *Client) Disconnect() error {
	c.disconnect(false)
	return nil
}

func (c *Client) refreshCredentials() error {
	var handler RefreshHandler
	if c.events != nil && c.events.onRefresh != nil {
		handler = c.events.onRefresh
	}
	if handler == nil {
		return errors.New("RefreshHandler must be set to handle expired credentials")
	}

	creds, err := handler.OnRefresh(c)
	if err != nil {
		return err
	}
	c.mutex.Lock()
	c.credentials = &creds
	c.mutex.Unlock()
	return nil
}

func (c *Client) sendRefresh() error {

	err := c.refreshCredentials()
	if err != nil {
		return err
	}

	c.mutex.RLock()
	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypeRefresh,
	}
	params := &proto.RefreshRequest{
		Credentials: &proto.SignedCredentials{
			User: c.credentials.User,
			Exp:  c.credentials.Exp,
			Info: c.credentials.Info,
			Sign: c.credentials.Sign,
		},
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		c.mutex.RUnlock()
		return err
	}
	cmd.Params = paramsData
	c.mutex.RUnlock()

	r, err := c.sendSync(cmd)
	if err != nil {
		return err
	}
	if r.Error != nil {
		return r.Error
	}
	var res proto.RefreshResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return err
	}
	if res.Expires {
		if res.Expired {
			return ErrClientExpired
		}
		go func(interval uint32) {
			tick := time.After(time.Duration(interval) * time.Second)
			select {
			case <-c.closeCh:
				return
			case <-tick:
				c.sendRefresh()
			}
		}(res.TTL)
	}
	return nil
}

func (c *Client) sendConnect() (proto.ConnectResult, error) {
	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypeConnect,
	}

	c.mutex.RLock()
	if c.credentials != nil || c.connectData != nil {
		params := &proto.ConnectRequest{}
		if c.credentials != nil {
			params.Credentials = &proto.SignedCredentials{
				User: c.credentials.User,
				Exp:  c.credentials.Exp,
				Info: c.credentials.Info,
				Sign: c.credentials.Sign,
			}
		}
		if c.connectData != nil {
			params.Data = c.connectData
		}
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			c.mutex.RUnlock()
			return proto.ConnectResult{}, err
		}
		cmd.Params = paramsData
	}
	c.mutex.RUnlock()

	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.ConnectResult{}, err
	}
	if r.Error != nil {
		return proto.ConnectResult{}, r.Error
	}

	var res proto.ConnectResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return proto.ConnectResult{}, err
	}
	return res, nil
}

func (c *Client) privateSign(channel string) (*PrivateSign, error) {
	var ps *PrivateSign
	if strings.HasPrefix(channel, c.config.PrivateChannelPrefix) && c.events != nil {
		handler := c.events.onPrivateSub
		if handler != nil {
			ev := PrivateSubEvent{
				ClientID: c.clientID(),
				Channel:  channel,
			}
			privateSign, err := handler.OnPrivateSub(c, ev)
			if err != nil {
				return nil, err
			}
			ps = &privateSign
		} else {
			return nil, errors.New("PrivateSubHandler must be set to handle private channel subscriptions")
		}
	}
	return ps, nil
}

// Subscribe allows to subscribe on channel.
func (c *Client) Subscribe(channel string, events *SubscriptionEventHub) *Subscription {
	c.subsMutex.Lock()
	var sub *Subscription
	if _, ok := c.subs[channel]; ok {
		sub = c.subs[channel]
		sub.events = events
	} else {
		sub = c.newSubscription(channel, events)
	}
	c.subs[channel] = sub
	c.subsMutex.Unlock()

	go func() {
		err := sub.resubscribe()
		if err != nil {
			c.disconnect(true)
		}
	}()
	return sub
}

// SubscribeSync allows to subscribe on channel and wait until subscribe success or error.
func (c *Client) SubscribeSync(channel string, events *SubscriptionEventHub) (*Subscription, error) {
	c.subsMutex.Lock()
	var sub *Subscription
	if _, ok := c.subs[channel]; ok {
		sub = c.subs[channel]
		sub.events = events
	} else {
		sub = c.newSubscription(channel, events)
	}
	c.subs[channel] = sub
	c.subsMutex.Unlock()

	err := sub.resubscribe()
	return sub, err
}

func (c *Client) subscribe(sub *Subscription) error {

	channel := sub.Channel()

	privateSign, err := c.privateSign(channel)
	if err != nil {
		return err
	}
	sub.lastMessageMu.Lock()
	res, err := c.sendSubscribe(channel, sub.lastMessageID, privateSign)
	sub.lastMessageMu.Unlock()

	c.mutex.Lock()
	defer c.mutex.Unlock()

	if err != nil {
		c.subsMutex.Lock()
		delete(c.subs, channel)
		c.subsMutex.Unlock()
		return err
	}

	sub.recover(res)

	return nil
}

func (c *Client) sendSubscribe(channel string, lastMessageID *string, privateSign *PrivateSign) (proto.SubscribeResult, error) {
	params := &proto.SubscribeRequest{
		Channel: channel,
	}

	if lastMessageID != nil {
		params.Recover = true
		params.Last = *lastMessageID
	}
	if privateSign != nil {
		params.Client = c.clientID()
		params.Info = privateSign.Info
		params.Sign = privateSign.Sign
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return proto.SubscribeResult{}, err
	}

	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypeSubscribe,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.SubscribeResult{}, err
	}
	if r.Error != nil {
		return proto.SubscribeResult{}, r.Error
	}

	var res proto.SubscribeResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return proto.SubscribeResult{}, err
	}
	return res, nil
}

func (c *Client) publish(channel string, data []byte) error {
	_, err := c.sendPublish(channel, data)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) sendPublish(channel string, data []byte) (proto.PublishResult, error) {
	params := &proto.PublishRequest{
		Channel: channel,
		Data:    proto.Raw(data),
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return proto.PublishResult{}, err
	}

	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypePublish,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.PublishResult{}, err
	}
	if r.Error != nil {
		return proto.PublishResult{}, r.Error
	}
	var res proto.PublishResult
	return res, nil
}

func (c *Client) history(channel string) ([]proto.Publication, error) {
	res, err := c.sendHistory(channel)
	if err != nil {
		return []proto.Publication{}, err
	}
	pubs := make([]proto.Publication, len(res.Publications))
	for i, m := range res.Publications {
		pubs[i] = *m
	}
	return pubs, nil
}

func (c *Client) sendHistory(channel string) (proto.HistoryResult, error) {
	params := &proto.HistoryRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return proto.HistoryResult{}, err
	}

	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypeHistory,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.HistoryResult{}, err
	}
	if r.Error != nil {
		return proto.HistoryResult{}, r.Error
	}
	var res proto.HistoryResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return proto.HistoryResult{}, err
	}
	return res, nil
}

func (c *Client) presence(channel string) (map[string]proto.ClientInfo, error) {
	res, err := c.sendPresence(channel)
	if err != nil {
		return map[string]proto.ClientInfo{}, err
	}
	p := make(map[string]proto.ClientInfo)
	for uid, info := range res.Presence {
		p[uid] = *info
	}
	return p, nil
}

func (c *Client) sendPresence(channel string) (proto.PresenceResult, error) {
	params := &proto.PresenceRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return proto.PresenceResult{}, err
	}

	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypePresence,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.PresenceResult{}, err
	}
	if r.Error != nil {
		return proto.PresenceResult{}, r.Error
	}
	var res proto.PresenceResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return proto.PresenceResult{}, err
	}
	return res, nil
}

func (c *Client) unsubscribe(channel string) error {
	if !c.subscribed(channel) {
		return nil
	}
	_, err := c.sendUnsubscribe(channel)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) sendUnsubscribe(channel string) (proto.UnsubscribeResult, error) {
	params := &proto.UnsubscribeRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return proto.UnsubscribeResult{}, err
	}

	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypeUnsubscribe,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.UnsubscribeResult{}, err
	}
	if r.Error != nil {
		return proto.UnsubscribeResult{}, r.Error
	}
	var res proto.UnsubscribeResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return proto.UnsubscribeResult{}, err
	}
	return res, nil
}

func (c *Client) sendPing() error {
	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypePing,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return err
	}
	if r.Error != nil {
		return r.Error
	}
	return nil
}

func (c *Client) sendSync(cmd *proto.Command) (proto.Reply, error) {
	waitCh := make(chan proto.Reply, 1)

	c.addRequest(cmd.ID, waitCh)
	defer c.removeRequest(cmd.ID)

	err := c.send(cmd)
	if err != nil {
		return proto.Reply{}, err
	}
	return c.wait(waitCh, c.config.ReadTimeout)
}

func (c *Client) send(cmd *proto.Command) error {
	select {
	case <-c.closeCh:
		return ErrClientDisconnected
	default:
		c.write <- cmd
	}
	return nil
}

func (c *Client) addRequest(id uint32, ch chan proto.Reply) {
	c.requestsMutex.Lock()
	defer c.requestsMutex.Unlock()
	c.requests[id] = ch
}

func (c *Client) removeRequest(id uint32) {
	c.requestsMutex.Lock()
	defer c.requestsMutex.Unlock()
	delete(c.requests, id)
}

func (c *Client) wait(ch chan proto.Reply, timeout time.Duration) (proto.Reply, error) {
	select {
	case data, ok := <-ch:
		if !ok {
			return proto.Reply{}, ErrClientDisconnected
		}
		return data, nil
	case <-time.After(timeout):
		return proto.Reply{}, ErrTimeout
	case <-c.closeCh:
		return proto.Reply{}, ErrClientClosed
	}
}
