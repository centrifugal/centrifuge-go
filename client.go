package centrifuge

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge-go/internal/proto"
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

// Client describes client connection to Centrifugo server.
type Client struct {
	mutex             sync.RWMutex
	url               string
	encoding          proto.Encoding
	config            Config
	token             string
	connectData       proto.Raw
	transport         transport
	msgID             uint32
	status            int
	id                string
	subsMutex         sync.RWMutex
	subs              map[string]*Subscription
	requestsMutex     sync.RWMutex
	requests          map[uint32]chan proto.Reply
	receive           chan []byte
	closeCh           chan struct{}
	reconnect         bool
	reconnectStrategy reconnectStrategy
	events            *EventHub
	paramsEncoder     proto.ParamsEncoder
	resultDecoder     proto.ResultDecoder
	commandEncoder    proto.CommandEncoder
	pushEncoder       proto.PushEncoder
	pushDecoder       proto.PushDecoder
	delayPing         chan struct{}
}

func (c *Client) nextMsgID() uint32 {
	return atomic.AddUint32(&c.msgID, 1)
}

// New initializes Client.
func New(u string, config Config) *Client {
	var encoding proto.Encoding

	if strings.HasPrefix(u, "ws") {
		if strings.Contains(u, "format=protobuf") {
			encoding = proto.EncodingProtobuf
		} else {
			encoding = proto.EncodingJSON
		}
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
		paramsEncoder:     proto.NewParamsEncoder(encoding),
		resultDecoder:     proto.NewResultDecoder(encoding),
		commandEncoder:    proto.NewCommandEncoder(encoding),
		pushEncoder:       proto.NewPushEncoder(encoding),
		pushDecoder:       proto.NewPushDecoder(encoding),
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
func (c *Client) SetConnectData(data proto.Raw) {
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
		ID:     c.nextMsgID(),
		Method: proto.MethodTypeRPC,
	}
	params := &proto.RPCRequest{
		Data: data,
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
	var res proto.RPCResult
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
	if c.status == DISCONNECTED || c.status == CLOSED || c.status == RECONNECTING {
		c.mutex.Unlock()
		return
	}

	c.reconnect = d.Reconnect

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

	if handler != nil {
		handler.OnDisconnect(c, DisconnectEvent{Reason: d.Reason, Reconnect: reconnect})
	}

	if !reconnect {
		return
	}

	go func() {
		err := c.reconnectStrategy.reconnect(c)
		if err != nil {
			c.handleError(err)
			c.Close()
		}
	}()
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
	MaxMilliseconds: 20 * 1000,
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
		c.mutex.RLock()
		reconnect := c.reconnect
		c.mutex.RUnlock()
		if !reconnect {
			return nil
		}
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
	err := c.connect(true)
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
				go c.handleDisconnect(&disconnect{Reason: "no ping", Reconnect: true})
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
			err := c.handle(reply)
			if err != nil {
				c.handleError(err)
			}
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
	if c.status == CONNECTED || c.status == CONNECTING || c.status == RECONNECTING {
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

	err := c.connect(false)
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
		c.close()
		return nil
	}

	return nil
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

	go c.reader(t, closeCh)

	var res proto.ConnectResult

	res, err = c.sendConnect()
	if err != nil {
		refreshed := false
		if e, ok := err.(*Error); ok {
			if e.Code == 109 {
				// Try to refresh token and repeat connection attempt.
				err = c.refreshToken()
				if err != nil {
					c.Close()
					return err
				}
				res, err = c.sendConnect()
				if err != nil {
					c.Close()
					return err
				}
				refreshed = true
			}
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
				c.sendRefresh(closeCh)
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
	c.disconnect(false)
	return nil
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
	cmd := &proto.Command{
		ID:     c.nextMsgID(),
		Method: proto.MethodTypeRefresh,
	}
	params := &proto.RefreshRequest{
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
	var res proto.RefreshResult
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
				c.sendRefresh(closeCh)
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

	token, err := c.privateSign(channel)
	if err != nil {
		return err
	}

	c.mutex.RLock()
	cmd := &proto.Command{
		ID:     c.nextMsgID(),
		Method: proto.MethodTypeSubRefresh,
	}
	params := &proto.SubRefreshRequest{
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
	var res proto.SubRefreshResult
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
				c.sendSubRefresh(channel)
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
	if c.token != "" || c.connectData != nil {
		params := &proto.ConnectRequest{}
		if c.token != "" {
			params.Token = c.token
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

func (c *Client) sendSubscribe(channel string, recover bool, seq uint32, gen uint32, epoch string, token string) (proto.SubscribeResult, error) {
	params := &proto.SubscribeRequest{
		Channel: channel,
	}

	if recover {
		params.Recover = true
		params.Seq = seq
		params.Gen = gen
		params.Epoch = epoch
	}
	if token != "" {
		params.Token = token
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

// Publish data into channel.
func (c *Client) Publish(channel string, data []byte) error {
	return c.publish(channel, data)
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

func (c *Client) sendPresenceStats(channel string) (proto.PresenceStatsResult, error) {
	params := &proto.PresenceStatsRequest{
		Channel: channel,
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		return proto.PresenceStatsResult{}, err
	}

	cmd := &proto.Command{
		ID:     uint32(c.nextMsgID()),
		Method: proto.MethodTypePresenceStats,
		Params: paramsData,
	}
	r, err := c.sendSync(cmd)
	if err != nil {
		return proto.PresenceStatsResult{}, err
	}
	if r.Error != nil {
		return proto.PresenceStatsResult{}, r.Error
	}
	var res proto.PresenceStatsResult
	err = c.resultDecoder.Decode(r.Result, &res)
	if err != nil {
		return proto.PresenceStatsResult{}, err
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
		err := c.transport.Write(cmd, c.config.WriteTimeout)
		if err != nil {
			go c.handleDisconnect(&disconnect{Reason: "write error", Reconnect: true})
			return err
		}
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
