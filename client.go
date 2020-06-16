package centrifuge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/protocol"
	"github.com/jpillora/backoff"
)

type disconnect struct {
	Reason    string
	Reconnect bool
}

// Describe client connection statuses.
const (
	DISCONNECTED = iota
	CONNECTING
	CONNECTED
	RECONNECTING
	CLOSED
)

type serverSub struct {
	Offset      uint64
	Epoch       string
	Recoverable bool
}

// Client describes client connection to Centrifugo server.
type Client struct {
	mutex             sync.RWMutex
	url               string
	encoding          protocol.Type
	config            Config
	token             string
	connectData       protocol.Raw
	transport         transport
	msgID             uint32
	status            int
	id                string
	subsMutex         sync.RWMutex
	subs              map[string]*Subscription
	serverSubs        map[string]serverSub
	requestsMutex     sync.RWMutex
	requests          map[uint32]chan protocol.Reply
	receive           chan []byte
	closeCh           chan struct{}
	reconnect         bool
	reconnectAttempts int
	reconnectStrategy reconnectStrategy
	events            *EventHub
	paramsEncoder     protocol.ParamsEncoder
	resultDecoder     protocol.ResultDecoder
	commandEncoder    protocol.CommandEncoder
	pushEncoder       protocol.PushEncoder
	pushDecoder       protocol.PushDecoder
	delayPing         chan struct{}
}

func (c *Client) nextMsgID() uint32 {
	return atomic.AddUint32(&c.msgID, 1)
}

func newPushEncoder(enc protocol.Type) protocol.PushEncoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONPushEncoder()
	}
	return protocol.NewProtobufPushEncoder()
}

func newPushDecoder(enc protocol.Type) protocol.PushDecoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONPushDecoder()
	}
	return protocol.NewProtobufPushDecoder()
}

func newReplyDecoder(enc protocol.Type, data []byte) protocol.ReplyDecoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONReplyDecoder(data)
	}
	return protocol.NewProtobufReplyDecoder(data)
}

func newResultDecoder(enc protocol.Type) protocol.ResultDecoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONResultDecoder()
	}
	return protocol.NewProtobufResultDecoder()
}

func newParamsEncoder(enc protocol.Type) protocol.ParamsEncoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONParamsEncoder()
	}
	return protocol.NewProtobufParamsEncoder()
}

func newCommandEncoder(enc protocol.Type) protocol.CommandEncoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONCommandEncoder()
	}
	return protocol.NewProtobufCommandEncoder()
}

// New initializes Client.
func New(u string, config Config) *Client {
	var encoding protocol.Type

	if strings.HasPrefix(u, "ws") {
		if strings.Contains(u, "format=protobuf") {
			encoding = protocol.TypeProtobuf
		} else {
			encoding = protocol.TypeJSON
		}
	} else {
		panic(fmt.Sprintf("unsupported connection endpoint: %s", u))
	}

	c := &Client{
		url:               u,
		encoding:          encoding,
		subs:              make(map[string]*Subscription),
		serverSubs:        make(map[string]serverSub),
		config:            config,
		requests:          make(map[uint32]chan protocol.Reply),
		reconnect:         true,
		reconnectStrategy: defaultBackoffReconnect,
		paramsEncoder:     newParamsEncoder(encoding),
		resultDecoder:     newResultDecoder(encoding),
		commandEncoder:    newCommandEncoder(encoding),
		pushEncoder:       newPushEncoder(encoding),
		pushDecoder:       newPushDecoder(encoding),
		delayPing:         make(chan struct{}, 32),
		events:            newEventHub(),
	}
	return c
}

// SetToken allows to set connection JWT token to let client
// authenticate itself on connect.
func (c *Client) SetToken(token string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.token = token
}

// SetConnectData allows to set data to send in connect message.
func (c *Client) SetConnectData(data protocol.Raw) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.connectData = data
}

// SetHeader allows to set custom header sent in Upgrade HTTP request.
func (c *Client) SetHeader(key, value string) {
	if c.config.Header == nil {
		c.config.Header = http.Header{}
	}
	c.config.Header.Set(key, value)
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
	cmd := &protocol.Command{
		Method: protocol.MethodTypeSend,
	}
	params := &protocol.SendRequest{
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
	return c.NamedRPC("", data)
}

// NamedRPC allows to make RPC – send data to server ant wait for response.
// RPC handler must be registered on server.
// In contrast to RPC method it allows to pass method name.
func (c *Client) NamedRPC(method string, data []byte) ([]byte, error) {
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeRPC,
	}
	params := &protocol.RPCRequest{
		Data:   data,
		Method: method,
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return nil, fmt.Errorf("encode error: %v", err)
	}
	cmd.Params = paramsData
	r, err := c.sendSync(cmd)
	if err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, r.Error
	}
	var res protocol.RPCResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// Close closes Client connection and cleans up state.
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
		_ = c.transport.Close()
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

	isReconnecting := c.status == RECONNECTING

	c.reconnect = d.Reconnect

	c.requestsMutex.Lock()
	for uid, ch := range c.requests {
		close(ch)
		delete(c.requests, uid)
	}
	c.requestsMutex.Unlock()

	if c.transport != nil {
		_ = c.transport.Close()
		c.transport = nil
	}

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
		if c.reconnect {
			s.mu.Lock()
			s.recover = true
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.recover = false
			s.mu.Unlock()
		}
	}

	reconnect := c.reconnect
	c.mutex.Unlock()

	var handler DisconnectHandler
	if c.events != nil && c.events.onDisconnect != nil {
		handler = c.events.onDisconnect
	}

	if handler != nil && !isReconnecting {
		handler.OnDisconnect(c, DisconnectEvent{Reason: d.Reason, Reconnect: reconnect})
	}

	if !reconnect {
		return
	}

	go func() {
		c.mutex.Lock()
		duration, err := c.reconnectStrategy.timeBeforeNextAttempt(c.reconnectAttempts)
		c.mutex.Unlock()
		if err != nil {
			c.handleError(err)
			return
		}
		time.Sleep(duration)
		c.mutex.Lock()
		c.reconnectAttempts++
		if !c.reconnect {
			c.mutex.Unlock()
			return
		}
		c.mutex.Unlock()
		err = c.connectFromScratch(true)
		if err != nil {
			c.handleError(err)
		}
	}()
}

type reconnectStrategy interface {
	timeBeforeNextAttempt(attempt int) (time.Duration, error)
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
	MaxMilliseconds: 20 * 1000,
	Factor:          2,
	Jitter:          true,
}

func (r *backoffReconnect) timeBeforeNextAttempt(attempt int) (time.Duration, error) {
	b := &backoff.Backoff{
		Min:    time.Duration(r.MinMilliseconds) * time.Millisecond,
		Max:    time.Duration(r.MaxMilliseconds) * time.Millisecond,
		Factor: r.Factor,
		Jitter: r.Jitter,
	}
	if r.NumReconnect > 0 && attempt >= r.NumReconnect {
		return 0, ErrReconnectFailed
	}
	return b.ForAttempt(float64(attempt)), nil
}

func (c *Client) periodicPing(closeCh chan struct{}) {
	timeout := c.config.PingInterval
	for {
		select {
		case <-c.delayPing:
		case <-time.After(timeout):
			err := c.sendPing()
			if err != nil {
				go c.handleDisconnect(&disconnect{Reason: "no ping", Reconnect: true})
				return
			}
		case <-closeCh:
			return
		}
	}
}

func (c *Client) reader(t transport, syncReplyCh, asyncReplyCh chan *protocol.Reply, closeCh chan struct{}) {
	for {
		reply, disconnect, err := t.Read()
		if err != nil {
			go c.handleDisconnect(disconnect)
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
			err := c.handle(reply, syncReplyCh, asyncReplyCh, closeCh)
			if err != nil {
				c.handleError(err)
			}
		}
	}
}

func (c *Client) processSyncReplies(syncReplyCh chan *protocol.Reply, closeCh chan struct{}) {
	for {
		select {
		case reply := <-syncReplyCh:
			c.requestsMutex.RLock()
			if waiter, ok := c.requests[reply.ID]; ok {
				waiter <- *reply
			}
			c.requestsMutex.RUnlock()
		case <-closeCh:
			close(syncReplyCh)
			return
		}
	}
}

func (c *Client) processAsyncReplies(asyncReplyCh chan *protocol.Reply, closeCh chan struct{}) {
	for {
		select {
		case reply := <-asyncReplyCh:
			push, err := c.pushDecoder.Decode(reply.Result)
			if err != nil {
				c.handleError(err)
				continue
			}
			err = c.handlePush(*push)
			if err != nil {
				c.handleError(err)
			}
		case <-closeCh:
			close(asyncReplyCh)
			return
		}
	}
}

func (c *Client) handle(reply *protocol.Reply, syncReplyCh, asyncReplyCh chan *protocol.Reply, closeCh chan struct{}) error {
	if reply.ID > 0 {
		syncReplyCh <- reply
	} else {
		select {
		case asyncReplyCh <- reply:
		case <-closeCh:
			return nil
		}
	}
	return nil
}

func (c *Client) handleMessage(msg protocol.Message) error {

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

func (c *Client) handlePush(msg protocol.Push) error {
	switch msg.Type {
	case protocol.PushTypeMessage:
		m, err := c.pushDecoder.DecodeMessage(msg.Data)
		if err != nil {
			return err
		}
		_ = c.handleMessage(*m)
	case protocol.PushTypeUnsub:
		m, err := c.pushDecoder.DecodeUnsub(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[channel]
		c.subsMutex.RUnlock()
		if !ok {
			return nil
		}
		sub.handleUnsub(*m)
	case protocol.PushTypePublication:
		m, err := c.pushDecoder.DecodePublication(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[channel]
		c.subsMutex.RUnlock()
		if !ok {
			return c.handleServerPublication(channel, *m)
		}
		sub.handlePublication(*m)
	case protocol.PushTypeJoin:
		m, err := c.pushDecoder.DecodeJoin(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[channel]
		c.subsMutex.RUnlock()
		if !ok {
			return nil
		}
		sub.handleJoin(m.Info)
	case protocol.PushTypeLeave:
		m, err := c.pushDecoder.DecodeLeave(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.subsMutex.RLock()
		sub, ok := c.subs[channel]
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

func (c *Client) handleServerPublication(channel string, pub Publication) error {
	c.subsMutex.Lock()
	_, ok := c.serverSubs[channel]
	c.subsMutex.Unlock()
	if !ok {
		return nil
	}

	var handler ServerPublishHandler
	if c.events != nil && c.events.onServerPublish != nil {
		handler = c.events.onServerPublish
	}
	if handler != nil {
		handler.OnServerPublish(c, ServerPublishEvent{Channel: channel, Publication: pub})
	}
	c.subsMutex.Lock()
	serverSub, ok := c.serverSubs[channel]
	if !ok {
		c.subsMutex.Unlock()
		return nil
	}
	serverSub.Offset = pub.Offset
	c.subsMutex.Unlock()
	return nil
}

func (c *Client) connectFromScratch(isReconnect bool) error {
	c.mutex.Lock()
	if c.status == CONNECTED || c.status == CONNECTING || c.status == RECONNECTING {
		c.mutex.Unlock()
		return nil
	}
	if c.status == CLOSED {
		c.mutex.Unlock()
		return ErrClientClosed
	}
	if isReconnect {
		c.status = RECONNECTING
	} else {
		c.status = CONNECTING
	}
	c.reconnect = true
	c.mutex.Unlock()

	err := c.connect(isReconnect)
	if err != nil {
		if c.transport == nil {
			c.handleError(err)
			go c.handleDisconnect(nil)
		}
		return nil
	}
	err = c.resubscribe()
	if err != nil {
		// we need just to close the connection and outgoing requests here
		// but preserve all subscriptions.
		c.handleError(err)
		c.close()
		return nil
	}

	// Looks like we successfully reconnected so can reset reconnect attempts.
	c.mutex.Lock()
	c.reconnectAttempts = 0
	c.mutex.Unlock()

	return nil
}

// Connect dials to server and sends connect message.
func (c *Client) Connect() error {
	return c.connectFromScratch(false)
}

func isTokenExpiredError(err error) bool {
	if e, ok := err.(*Error); ok && e.Code == 109 {
		return true
	}
	return false
}

func (c *Client) connect(isReconnect bool) error {
	c.mutex.Lock()
	if c.status == CONNECTED {
		c.mutex.Unlock()
		return nil
	}
	if isReconnect {
		c.status = RECONNECTING
	} else {
		c.status = CONNECTING
	}
	c.closeCh = make(chan struct{})
	c.mutex.Unlock()
	wsConfig := websocketConfig{
		NetDialContext:    c.config.NetDialContext,
		TLSConfig:         c.config.TLSConfig,
		HandshakeTimeout:  c.config.HandshakeTimeout,
		EnableCompression: c.config.EnableCompression,
		CookieJar:         c.config.CookieJar,
		Header:            c.config.Header,
	}

	t, err := newWebsocketTransport(c.url, c.encoding, wsConfig)
	if err != nil {
		return err
	}

	c.mutex.Lock()
	if c.status == DISCONNECTED {
		c.mutex.Unlock()
		return nil
	}

	c.transport = t
	closeCh := make(chan struct{})
	c.closeCh = closeCh
	c.receive = make(chan []byte, 64)
	c.mutex.Unlock()

	syncReplyCh := make(chan *protocol.Reply)
	asyncReplyCh := make(chan *protocol.Reply, 128)

	go c.reader(t, syncReplyCh, asyncReplyCh, closeCh)
	go c.processSyncReplies(syncReplyCh, closeCh)
	go c.processAsyncReplies(asyncReplyCh, closeCh)

	var res protocol.ConnectResult

	res, err = c.sendConnect()
	if err != nil {
		refreshed := false
		if isTokenExpiredError(err) {
			// Try to refresh token and repeat connection attempt.
			err = c.refreshToken()
			if err != nil {
				_ = c.Close()
				return err
			}
			res, err = c.sendConnect()
			if err != nil {
				_ = c.Close()
				return err
			}
			refreshed = true
		}
		if !refreshed {
			return err
		}
	}

	c.mutex.Lock()
	c.id = res.Client
	prevStatus := c.status
	c.status = CONNECTED
	c.mutex.Unlock()

	if res.Expires {
		go func(interval uint32) {
			select {
			case <-closeCh:
				return
			case <-time.After(time.Duration(interval) * time.Second):
				_ = c.sendRefresh(closeCh)
			}
		}(res.TTL)
	}

	go c.periodicPing(closeCh)

	if c.events != nil && c.events.onConnect != nil && prevStatus != CONNECTED {
		handler := c.events.onConnect
		ev := ConnectEvent{
			ClientID: c.clientID(),
			Version:  res.Version,
			Data:     res.Data,
		}
		handler.OnConnect(c, ev)
	}

	c.processServerSubs(res.Subs)

	return nil
}

func (c *Client) processServerSubs(subs map[string]*protocol.SubscribeResult) {
	// TODO: call ServerSubscribeHandler.

	for channel, subRes := range subs {
		c.mutex.Lock()
		c.serverSubs[channel] = serverSub{
			Offset:      subRes.Offset,
			Epoch:       subRes.Epoch,
			Recoverable: subRes.Recoverable,
		}
		c.mutex.Unlock()
	}
}

func (c *Client) resubscribe() error {
	c.subsMutex.RLock()
	defer c.subsMutex.RUnlock()
	for _, sub := range c.subs {
		err := sub.resubscribe(true)
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
	return c.disconnect(false)
}

func (c *Client) refreshToken() error {
	var handler RefreshHandler
	if c.events != nil && c.events.onRefresh != nil {
		handler = c.events.onRefresh
	}
	if handler == nil {
		return errors.New("RefreshHandler must be set to handle expired token")
	}

	token, err := handler.OnRefresh(c)
	if err != nil {
		return err
	}
	c.mutex.Lock()
	c.token = token
	c.mutex.Unlock()
	return nil
}

func (c *Client) sendRefresh(closeCh chan struct{}) error {

	err := c.refreshToken()
	if err != nil {
		return err
	}

	c.mutex.RLock()
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeRefresh,
	}
	params := &protocol.RefreshRequest{
		Token: c.token,
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
	var res protocol.RefreshResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return err
	}
	if res.Expires {
		go func(interval uint32) {
			select {
			case <-closeCh:
				return
			case <-time.After(time.Duration(interval) * time.Second):
				_ = c.sendRefresh(closeCh)
			}
		}(res.TTL)
	}
	return nil
}

func (c *Client) sendSubRefresh(channel string) error {

	sub, ok := c.subs[channel]
	if !ok {
		return nil
	}

	if sub.Status() != SUBSCRIBED {
		return nil
	}

	c.mutex.RLock()
	clientID := c.id
	c.mutex.RUnlock()

	token, err := c.privateSign(channel)
	if err != nil {
		return err
	}

	c.mutex.RLock()
	if c.id != clientID {
		c.mutex.RUnlock()
		return nil
	}
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeSubRefresh,
	}
	params := &protocol.SubRefreshRequest{
		Channel: channel,
		Token:   token,
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
	var res protocol.SubRefreshResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return err
	}
	if res.Expires {
		if sub.Status() != SUBSCRIBED {
			return nil
		}
		go func(interval uint32) {
			select {
			case <-c.closeCh:
				return
			case <-time.After(time.Duration(interval) * time.Second):
				_ = c.sendSubRefresh(channel)
			}
		}(res.TTL)
	}
	return nil
}

func (c *Client) sendConnect() (protocol.ConnectResult, error) {
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeConnect,
	}

	c.mutex.RLock()
	if c.token != "" || c.connectData != nil {
		params := &protocol.ConnectRequest{}
		if c.token != "" {
			params.Token = c.token
		}
		if c.connectData != nil {
			params.Data = c.connectData
		}
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			c.mutex.RUnlock()
			return protocol.ConnectResult{}, err
		}
		cmd.Params = paramsData
	}
	c.mutex.RUnlock()

	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.ConnectResult{}, err
	}
	if r.Error != nil {
		return protocol.ConnectResult{}, r.Error
	}

	var res protocol.ConnectResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return protocol.ConnectResult{}, err
	}
	return res, nil
}

func (c *Client) privateSign(channel string) (string, error) {
	var token string
	if strings.HasPrefix(channel, c.config.PrivateChannelPrefix) && c.events != nil {
		handler := c.events.onPrivateSub
		if handler != nil {
			ev := PrivateSubEvent{
				ClientID: c.clientID(),
				Channel:  channel,
			}
			ps, err := handler.OnPrivateSub(c, ev)
			if err != nil {
				return "", err
			}
			token = ps
		} else {
			return "", errors.New("PrivateSubHandler must be set to handle private channel subscriptions")
		}
	}
	return token, nil
}

// NewSubscription allows to create new subscription on channel.
func (c *Client) NewSubscription(channel string) (*Subscription, error) {
	c.subsMutex.Lock()
	var sub *Subscription
	if _, ok := c.subs[channel]; ok {
		c.subsMutex.Unlock()
		return nil, ErrDuplicateSubscription
	}
	sub = c.newSubscription(channel)
	c.subs[channel] = sub
	c.subsMutex.Unlock()
	return sub, nil
}

type streamPosition struct {
	Seq    uint32
	Gen    uint32
	Offset uint64
	Epoch  string
}

func (c *Client) sendSubscribe(channel string, recover bool, streamPos streamPosition, token string) (protocol.SubscribeResult, error) {
	params := &protocol.SubscribeRequest{
		Channel: channel,
	}

	if recover {
		params.Recover = true
		if streamPos.Seq > 0 || streamPos.Gen > 0 {
			params.Seq = streamPos.Seq
			params.Gen = streamPos.Gen
		} else if streamPos.Offset > 0 {
			params.Offset = streamPos.Offset
		}
		params.Epoch = streamPos.Epoch
	}
	if token != "" {
		params.Token = token
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return protocol.SubscribeResult{}, err
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeSubscribe,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.SubscribeResult{}, err
	}
	if r.Error != nil {
		return protocol.SubscribeResult{}, r.Error
	}

	var res protocol.SubscribeResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return protocol.SubscribeResult{}, err
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

// Publish data into channel.
func (c *Client) Publish(channel string, data []byte) error {
	return c.publish(channel, data)
}

func (c *Client) sendPublish(channel string, data []byte) (protocol.PublishResult, error) {
	params := &protocol.PublishRequest{
		Channel: channel,
		Data:    protocol.Raw(data),
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return protocol.PublishResult{}, err
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePublish,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.PublishResult{}, err
	}
	if r.Error != nil {
		return protocol.PublishResult{}, r.Error
	}
	var res protocol.PublishResult
	return res, nil
}

func (c *Client) history(channel string) ([]protocol.Publication, error) {
	res, err := c.sendHistory(channel)
	if err != nil {
		return []protocol.Publication{}, err
	}
	pubs := make([]protocol.Publication, len(res.Publications))
	for i, m := range res.Publications {
		pubs[i] = *m
	}
	return pubs, nil
}

func (c *Client) sendHistory(channel string) (protocol.HistoryResult, error) {
	params := &protocol.HistoryRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return protocol.HistoryResult{}, err
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeHistory,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.HistoryResult{}, err
	}
	if r.Error != nil {
		return protocol.HistoryResult{}, r.Error
	}
	var res protocol.HistoryResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return protocol.HistoryResult{}, err
	}
	return res, nil
}

func (c *Client) presence(channel string) (map[string]protocol.ClientInfo, error) {
	res, err := c.sendPresence(channel)
	if err != nil {
		return map[string]protocol.ClientInfo{}, err
	}
	p := make(map[string]protocol.ClientInfo)
	for uid, info := range res.Presence {
		p[uid] = *info
	}
	return p, nil
}

func (c *Client) sendPresence(channel string) (protocol.PresenceResult, error) {
	params := &protocol.PresenceRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return protocol.PresenceResult{}, err
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePresence,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.PresenceResult{}, err
	}
	if r.Error != nil {
		return protocol.PresenceResult{}, r.Error
	}
	var res protocol.PresenceResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return protocol.PresenceResult{}, err
	}
	return res, nil
}

// PresenceStats represents short presence information.
type PresenceStats struct {
	NumClients int
	NumUsers   int
}

func (c *Client) presenceStats(channel string) (PresenceStats, error) {
	res, err := c.sendPresenceStats(channel)
	if err != nil {
		return PresenceStats{}, err
	}
	return PresenceStats{
		NumClients: int(res.NumClients),
		NumUsers:   int(res.NumUsers),
	}, nil
}

func (c *Client) sendPresenceStats(channel string) (protocol.PresenceStatsResult, error) {
	params := &protocol.PresenceStatsRequest{
		Channel: channel,
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return protocol.PresenceStatsResult{}, err
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePresenceStats,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.PresenceStatsResult{}, err
	}
	if r.Error != nil {
		return protocol.PresenceStatsResult{}, r.Error
	}
	var res protocol.PresenceStatsResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return protocol.PresenceStatsResult{}, err
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

func (c *Client) sendUnsubscribe(channel string) (protocol.UnsubscribeResult, error) {
	params := &protocol.UnsubscribeRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return protocol.UnsubscribeResult{}, err
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeUnsubscribe,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return protocol.UnsubscribeResult{}, err
	}
	if r.Error != nil {
		return protocol.UnsubscribeResult{}, r.Error
	}
	var res protocol.UnsubscribeResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return protocol.UnsubscribeResult{}, err
	}
	return res, nil
}

func (c *Client) sendPing() error {
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePing,
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

func (c *Client) sendSync(cmd *protocol.Command) (protocol.Reply, error) {
	waitCh := make(chan protocol.Reply, 1)

	c.addRequest(cmd.ID, waitCh)
	defer c.removeRequest(cmd.ID)

	err := c.send(cmd)
	if err != nil {
		return protocol.Reply{}, err
	}
	return c.wait(waitCh, c.config.ReadTimeout)
}

func (c *Client) send(cmd *protocol.Command) error {
	select {
	case <-c.closeCh:
		return ErrClientDisconnected
	default:
		c.mutex.Lock()
		transport := c.transport
		c.mutex.Unlock()
		if transport == nil {
			return ErrClientDisconnected
		}
		err := transport.Write(cmd, c.config.WriteTimeout)
		if err != nil {
			go c.handleDisconnect(&disconnect{Reason: "write error", Reconnect: true})
			return err
		}
	}
	return nil
}

func (c *Client) addRequest(id uint32, ch chan protocol.Reply) {
	c.requestsMutex.Lock()
	defer c.requestsMutex.Unlock()
	c.requests[id] = ch
}

func (c *Client) removeRequest(id uint32) {
	c.requestsMutex.Lock()
	defer c.requestsMutex.Unlock()
	delete(c.requests, id)
}

func (c *Client) wait(ch chan protocol.Reply, timeout time.Duration) (protocol.Reply, error) {
	select {
	case data, ok := <-ch:
		if !ok {
			return protocol.Reply{}, ErrClientDisconnected
		}
		return data, nil
	case <-time.After(timeout):
		return protocol.Reply{}, ErrTimeout
	case <-c.closeCh:
		return protocol.Reply{}, ErrClientClosed
	}
}
