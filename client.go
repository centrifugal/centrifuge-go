package centrifuge

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/protocol"
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

// Client describes client connection to Centrifugo or Centrifuge-based server.
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
	subs              map[string]*Subscription
	serverSubs        map[string]*serverSub
	requestsMu        sync.RWMutex
	requests          map[uint32]request
	receive           chan []byte
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
		status:            DISCONNECTED,
		encoding:          encoding,
		subs:              make(map[string]*Subscription),
		serverSubs:        make(map[string]*serverSub),
		config:            config,
		requests:          make(map[uint32]request),
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

// SetConnectData allows to set data to send in connect command.
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

func (c *Client) connected() bool {
	c.mutex.RLock()
	defer c.mutex.RUnlock()
	return c.status == CONNECTED
}

func (c *Client) subscribed(channel string) bool {
	c.mutex.RLock()
	_, ok := c.subs[channel]
	c.mutex.RUnlock()
	return ok
}

// clientID returns unique ID of this connection which is set by server after connect.
// It only available after connection was established and authorized.
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
		c.runHandler(func() {
			handler.OnError(c, ErrorEvent{Message: err.Error()})
		})
	}
}

// Send message to server without waiting for response.
// Message handler must be registered on server.
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

type RPCResult struct {
	Data []byte
}

// RPC allows to make RPC – send data to server and wait for response.
// RPC handler must be registered on server.
func (c *Client) RPC(data []byte, fn func(RPCResult, error)) {
	c.NamedRPC("", data, fn)
}

// NamedRPC allows to make RPC – send data to server ant wait for response.
// RPC handler must be registered on server.
// In contrast to RPC method it allows to pass method name.
func (c *Client) NamedRPC(method string, data []byte, fn func(RPCResult, error)) {
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
		fn(RPCResult{}, fmt.Errorf("encode error: %v", err))
		return
	}
	cmd.Params = paramsData
	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(RPCResult{}, err)
			return
		}
		if r.Error != nil {
			fn(RPCResult{}, r.Error)
			return
		}
		var res protocol.RPCResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			fn(RPCResult{}, err)
			return
		}
		fn(RPCResult{res.Data}, nil)
	})
}

// Close closes Client connection and cleans up state.
func (c *Client) Close() error {
	err := c.Disconnect()
	c.mutex.Lock()
	c.status = CLOSED
	c.mutex.Unlock()
	return err
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

	c.requestsMu.Lock()
	reqs := make(map[uint32]request, len(c.requests))
	for uid, req := range c.requests {
		reqs[uid] = req
	}
	c.requests = make(map[uint32]request)
	c.requestsMu.Unlock()

	if c.transport != nil {
		_ = c.transport.Close()
		c.transport = nil
	}

	unsubs := make([]*Subscription, 0, len(c.subs))
	for _, s := range c.subs {
		unsubs = append(unsubs, s)
	}

	needDisconnectEvent := c.status == CONNECTING || c.status == CONNECTED
	c.reconnect = d.Reconnect
	if c.reconnect {
		c.status = RECONNECTING
	} else {
		c.status = DISCONNECTED
	}
	c.mutex.Unlock()

	for _, req := range reqs {
		if req.ch != nil {
			close(req.ch)
		}
		if req.cb != nil {
			req.cb(protocol.Reply{}, ErrClientDisconnected)
		}
	}

	for _, s := range unsubs {
		s.triggerOnUnsubscribe(true)
		if d.Reconnect {
			s.mu.Lock()
			s.recover = true
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.recover = false
			s.mu.Unlock()
		}
	}

	var handler DisconnectHandler
	if c.events != nil && c.events.onDisconnect != nil {
		handler = c.events.onDisconnect
	}

	if handler != nil && needDisconnectEvent {
		c.runHandler(func() {
			handler.OnDisconnect(c, DisconnectEvent{Reason: d.Reason, Reconnect: d.Reconnect})
		})
	}

	if !d.Reconnect {
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

func (c *Client) periodicPing(closeCh chan struct{}) {
	timeout := c.config.PingInterval
	for {
		select {
		case <-c.delayPing:
		case <-time.After(timeout):
			c.sendPing(func(err error) {
				if err != nil {
					go c.handleDisconnect(&disconnect{Reason: "no ping", Reconnect: true})
					return
				}
			})
		case <-closeCh:
			return
		}
	}
}

func (c *Client) readOnce(t transport) error {
	reply, disconnect, err := t.Read()
	if err != nil {
		go c.handleDisconnect(disconnect)
		return err
	}
	select {
	case c.delayPing <- struct{}{}:
	default:
	}
	err = c.handle(reply)
	if err != nil {
		c.handleError(err)
	}
	return nil
}

func (c *Client) reader(t transport, closeCh chan struct{}) {
	defer close(closeCh)
	for {
		err := c.readOnce(t)
		if err != nil {
			return
		}
	}
}

func (c *Client) runHandler(fn func()) {
	fn()
}

func (c *Client) handle(reply *protocol.Reply) error {
	if reply.ID > 0 {
		c.requestsMu.RLock()
		req, ok := c.requests[reply.ID]
		c.requestsMu.RUnlock()
		if ok {
			if req.ch != nil {
				req.ch <- *reply
			}
			if req.cb != nil {
				req.cb(*reply, nil)
			}
		}
		c.removeRequest(reply.ID)
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

func (c *Client) handleMessage(msg protocol.Message) error {

	var handler MessageHandler
	if c.events != nil && c.events.onMessage != nil {
		handler = c.events.onMessage
	}

	if handler != nil {
		event := MessageEvent{Data: msg.Data}
		c.runHandler(func() {
			handler.OnMessage(c, event)
		})
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
		c.mutex.RLock()
		sub, ok := c.subs[channel]
		c.mutex.RUnlock()
		if !ok {
			return c.handleServerUnsub(channel, *m)
		}
		sub.handleUnsub(*m)
	case protocol.PushTypePublication:
		m, err := c.pushDecoder.DecodePublication(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.mutex.RLock()
		sub, ok := c.subs[channel]
		c.mutex.RUnlock()
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
		c.mutex.RLock()
		sub, ok := c.subs[channel]
		c.mutex.RUnlock()
		if !ok {
			return c.handleServerJoin(channel, *m)
		}
		sub.handleJoin(m.Info)
	case protocol.PushTypeLeave:
		m, err := c.pushDecoder.DecodeLeave(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.mutex.RLock()
		sub, ok := c.subs[channel]
		c.mutex.RUnlock()
		if !ok {
			return c.handleServerLeave(channel, *m)
		}
		sub.handleLeave(m.Info)
	default:
		return nil
	}
	return nil
}

func (c *Client) handleServerPublication(channel string, pub Publication) error {
	c.mutex.RLock()
	_, ok := c.serverSubs[channel]
	c.mutex.RUnlock()
	if !ok {
		return nil
	}

	var handler ServerPublishHandler
	if c.events != nil && c.events.onServerPublish != nil {
		handler = c.events.onServerPublish
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerPublish(c, ServerPublishEvent{Channel: channel, Publication: pub})
		})
	}
	c.mutex.Lock()
	serverSub, ok := c.serverSubs[channel]
	if !ok {
		c.mutex.Unlock()
		return nil
	}
	serverSub.Offset = pub.Offset
	c.mutex.Unlock()
	return nil
}

func (c *Client) handleServerJoin(channel string, join protocol.Join) error {
	c.mutex.RLock()
	_, ok := c.serverSubs[channel]
	c.mutex.RUnlock()
	if !ok {
		return nil
	}

	var handler ServerJoinHandler
	if c.events != nil && c.events.onServerJoin != nil {
		handler = c.events.onServerJoin
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerJoin(c, ServerJoinEvent{Channel: channel, ClientInfo: join.Info})
		})
	}
	return nil
}

func (c *Client) handleServerLeave(channel string, leave protocol.Leave) error {
	c.mutex.RLock()
	_, ok := c.serverSubs[channel]
	c.mutex.RUnlock()
	if !ok {
		return nil
	}

	var handler ServerLeaveHandler
	if c.events != nil && c.events.onServerLeave != nil {
		handler = c.events.onServerLeave
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerLeave(c, ServerLeaveEvent{Channel: channel, ClientInfo: leave.Info})
		})
	}
	return nil
}

func (c *Client) handleServerUnsub(channel string, _ protocol.Unsub) error {
	c.mutex.RLock()
	_, ok := c.serverSubs[channel]
	c.mutex.RUnlock()
	if !ok {
		return nil
	}

	var handler ServerUnsubscribeHandler
	if c.events != nil && c.events.onServerUnsubscribe != nil {
		handler = c.events.onServerUnsubscribe
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerUnsubscribe(c, ServerUnsubscribeEvent{Channel: channel})
		})
	}
	return nil
}

// Connect dials to server and sends connect message. Will return an error if dialing
// with server failed. In case of failure client will try to reconnect with exponential
// backoff.
func (c *Client) Connect() error {
	return c.connectFromScratch(false)
}

func (c *Client) connectFromScratch(isReconnect bool) error {
	c.mutex.Lock()
	if isReconnect && c.status == DISCONNECTED {
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
		go c.handleDisconnect(&disconnect{Reason: "connect error", Reconnect: true})
		return err
	}

	c.mutex.Lock()
	if c.status == CONNECTED || c.status == DISCONNECTED || c.status == CLOSED {
		_ = t.Close()
		c.mutex.Unlock()
		return nil
	}

	closeCh := make(chan struct{})
	c.receive = make(chan []byte, 64)
	c.transport = t
	go c.reader(t, closeCh)
	c.sendConnect(isReconnect, func(res protocol.ConnectResult, err error) {
		go func() {
			c.mutex.Lock()
			if c.status == CONNECTED || c.status == DISCONNECTED || c.status == CLOSED {
				c.mutex.Unlock()
				return
			}
			if err != nil {
				if isTokenExpiredError(err) {
					c.mutex.Unlock()
					// Try to refresh token before next connection attempt.
					_ = c.refreshToken()
					c.mutex.Lock()
					if c.status == CONNECTED || c.status == DISCONNECTED || c.status == CLOSED {
						c.mutex.Unlock()
						return
					}
				}
				go c.handleDisconnect(&disconnect{Reason: "connect error", Reconnect: true})
				c.mutex.Unlock()
				return
			}

			c.id = res.Client
			prevStatus := c.status
			c.status = CONNECTED

			if res.Expires {
				go func(interval uint32, closeCh chan struct{}) {
					select {
					case <-closeCh:
						return
					case <-time.After(time.Duration(interval) * time.Second):
						c.sendRefresh(closeCh)
					}
				}(res.TTL, closeCh)
			}
			c.mutex.Unlock()

			if c.events != nil && c.events.onConnect != nil && prevStatus != CONNECTED {
				handler := c.events.onConnect
				ev := ConnectEvent{
					ClientID: c.clientID(),
					Version:  res.Version,
					Data:     res.Data,
				}
				c.runHandler(func() {
					handler.OnConnect(c, ev)
				})
			}

			var handler ServerSubscribeHandler
			if c.events != nil && c.events.onServerSubscribe != nil {
				handler = c.events.onServerSubscribe
			}

			var publishHandler ServerPublishHandler
			if c.events != nil && c.events.onServerPublish != nil {
				publishHandler = c.events.onServerPublish
			}

			for channel, subRes := range res.Subs {
				if handler != nil {
					c.runHandler(func() {
						handler.OnServerSubscribe(c, ServerSubscribeEvent{
							Channel:      channel,
							Resubscribed: isReconnect, // TODO: check request map.
							Recovered:    subRes.Recovered,
						})
					})
				}
				if publishHandler != nil {
					for _, pub := range subRes.Publications {
						c.runHandler(func() {
							publishHandler.OnServerPublish(c, ServerPublishEvent{Channel: channel, Publication: *pub})
						})
					}
				}
			}

			newServerSubs := make(map[string]*serverSub)
			for channel, subRes := range res.Subs {
				newServerSubs[channel] = &serverSub{
					Offset:      subRes.Offset,
					Epoch:       subRes.Epoch,
					Recoverable: subRes.Recoverable,
				}
			}

			c.mutex.Lock()
			defer c.mutex.Unlock()

			c.serverSubs = newServerSubs
			if c.status != CONNECTED {
				return
			}

			err = c.resubscribe()
			if err != nil {
				// we need just to close the connection and outgoing requests here
				// but preserve all subscriptions.
				go c.handleDisconnect(&disconnect{Reason: "subscribe error", Reconnect: true})
				return
			}

			// Successfully connected – can reset reconnect attempts.
			c.reconnectAttempts = 0

			go c.periodicPing(closeCh)
		}()
	})
	c.mutex.Unlock()
	return nil
}

func (c *Client) resubscribe() error {
	for _, sub := range c.subs {
		err := sub.resubscribe(true)
		if err != nil {
			return err
		}
	}
	return nil
}

func isTokenExpiredError(err error) bool {
	if e, ok := err.(*Error); ok && e.Code == 109 {
		return true
	}
	return false
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

func (c *Client) sendRefresh(closeCh chan struct{}) {

	err := c.refreshToken()
	if err != nil {
		return
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
		return
	}
	cmd.Params = paramsData
	c.mutex.RUnlock()

	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			return
		}
		if r.Error != nil {
			return
		}
		var res protocol.RefreshResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			return
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
	})
}

func (c *Client) sendSubRefresh(channel string, fn func(protocol.SubRefreshResult, error)) {
	sub, ok := c.subs[channel]
	if !ok {
		return
	}

	if sub.Status() != SUBSCRIBED {
		return
	}

	c.mutex.RLock()
	clientID := c.id
	c.mutex.RUnlock()

	token, err := c.privateSign(channel)
	if err != nil {
		return
	}

	c.mutex.RLock()
	if c.id != clientID {
		c.mutex.RUnlock()
		return
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
		fn(protocol.SubRefreshResult{}, err)
		return
	}
	cmd.Params = paramsData
	c.mutex.RUnlock()

	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(protocol.SubRefreshResult{}, err)
			return
		}
		if r.Error != nil {
			fn(protocol.SubRefreshResult{}, r.Error)
			return
		}
		var res protocol.SubRefreshResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			fn(protocol.SubRefreshResult{}, err)
			return
		}
		fn(res, nil)
	})
}

func (c *Client) sendConnect(isReconnect bool, fn func(protocol.ConnectResult, error)) {
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeConnect,
	}

	if c.token != "" || c.connectData != nil {
		params := &protocol.ConnectRequest{}
		if c.token != "" {
			params.Token = c.token
		}
		if c.connectData != nil {
			params.Data = c.connectData
		}
		if isReconnect && len(c.serverSubs) > 0 {
			subs := make(map[string]*protocol.SubscribeRequest)
			for channel, serverSub := range c.serverSubs {
				if !serverSub.Recoverable {
					continue
				}
				subs[channel] = &protocol.SubscribeRequest{
					Recover: true,
					Epoch:   serverSub.Epoch,
					Offset:  serverSub.Offset,
				}
			}
			params.Subs = subs
		}
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(protocol.ConnectResult{}, err)
			return
		}
		cmd.Params = paramsData
	}

	c.sendAsync(cmd, func(reply protocol.Reply, err error) {
		if err != nil {
			fn(protocol.ConnectResult{}, err)
			return
		}
		if reply.Error != nil {
			fn(protocol.ConnectResult{}, reply.Error)
			return
		}

		var res protocol.ConnectResult
		err = c.resultDecoder.Decode(reply.Result, &res)
		if err != nil {
			fn(protocol.ConnectResult{}, err)
			return
		}
		fn(res, nil)
	})
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
	c.mutex.Lock()
	defer c.mutex.Unlock()
	var sub *Subscription
	if _, ok := c.subs[channel]; ok {
		return nil, ErrDuplicateSubscription
	}
	sub = c.newSubscription(channel)
	c.subs[channel] = sub
	return sub, nil
}

type streamPosition struct {
	Seq    uint32
	Gen    uint32
	Offset uint64
	Epoch  string
}

func (c *Client) sendSubscribe(channel string, recover bool, streamPos streamPosition, token string, fn func(res protocol.SubscribeResult, err error)) {
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
		fn(protocol.SubscribeResult{}, err)
		return
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeSubscribe,
		Params: paramsData,
	}
	c.sendAsync(cmd, func(reply protocol.Reply, err error) {
		if err != nil {
			fn(protocol.SubscribeResult{}, err)
			return
		}
		if reply.Error != nil {
			fn(protocol.SubscribeResult{}, reply.Error)
			return
		}
		var res protocol.SubscribeResult
		err = c.resultDecoder.Decode(reply.Result, &res)
		if err != nil {
			fn(protocol.SubscribeResult{}, err)
			return
		}
		fn(res, nil)
	})
}

type PublishResult struct{}

// Publish data into channel.
func (c *Client) Publish(channel string, data []byte, fn func(PublishResult, error)) {
	c.publish(channel, data, fn)
}

func (c *Client) publish(channel string, data []byte, fn func(PublishResult, error)) {
	c.sendPublish(channel, data, fn)
}

func (c *Client) sendPublish(channel string, data []byte, fn func(PublishResult, error)) {
	params := &protocol.PublishRequest{
		Channel: channel,
		Data:    protocol.Raw(data),
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		fn(PublishResult{}, err)
		return
	}
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePublish,
		Params: paramsData,
	}
	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(PublishResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PublishResult{}, r.Error)
			return
		}
		fn(PublishResult{}, nil)
	})
}

type HistoryResult struct {
	Publications []protocol.Publication
}

func (c *Client) history(channel string, fn func(HistoryResult, error)) {
	c.sendHistory(channel, fn)
}

func (c *Client) sendHistory(channel string, fn func(HistoryResult, error)) {
	params := &protocol.HistoryRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		fn(HistoryResult{}, err)
		return
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeHistory,
		Params: paramsData,
	}
	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		if r.Error != nil {
			fn(HistoryResult{}, r.Error)
			return
		}
		var res protocol.HistoryResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		pubs := make([]protocol.Publication, len(res.Publications))
		for i, m := range res.Publications {
			pubs[i] = *m
		}
		fn(HistoryResult{Publications: pubs}, nil)
	})
}

type PresenceResult struct {
	Presence map[string]protocol.ClientInfo
}

func (c *Client) presence(channel string, fn func(PresenceResult, error)) {
	c.sendPresence(channel, fn)
}

func (c *Client) sendPresence(channel string, fn func(PresenceResult, error)) {
	params := &protocol.PresenceRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		fn(PresenceResult{}, err)
		return
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePresence,
		Params: paramsData,
	}
	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PresenceResult{}, r.Error)
			return
		}
		var res protocol.PresenceResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		p := make(map[string]protocol.ClientInfo)
		for uid, info := range res.Presence {
			p[uid] = *info
		}
		fn(PresenceResult{Presence: p}, nil)
	})
}

// PresenceStats represents short presence information.
type PresenceStats struct {
	NumClients int
	NumUsers   int
}

// PresenceStatsResult wraps presence stats.
type PresenceStatsResult struct {
	PresenceStats
}

func (c *Client) presenceStats(channel string, fn func(PresenceStatsResult, error)) {
	c.sendPresenceStats(channel, fn)
}

func (c *Client) sendPresenceStats(channel string, fn func(PresenceStatsResult, error)) {
	params := &protocol.PresenceStatsRequest{
		Channel: channel,
	}
	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		fn(PresenceStatsResult{}, err)
		return
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePresenceStats,
		Params: paramsData,
	}
	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PresenceStatsResult{}, r.Error)
			return
		}
		var res protocol.PresenceStatsResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		fn(PresenceStatsResult{PresenceStats{
			NumClients: int(res.NumClients),
			NumUsers:   int(res.NumUsers),
		}}, nil)
	})
}

type UnsubscribeResult struct{}

func (c *Client) unsubscribe(channel string, fn func(UnsubscribeResult, error)) {
	if !c.subscribed(channel) {
		return
	}
	c.sendUnsubscribe(channel, fn)
}

func (c *Client) sendUnsubscribe(channel string, fn func(UnsubscribeResult, error)) {
	params := &protocol.UnsubscribeRequest{
		Channel: channel,
	}

	paramsData, err := c.paramsEncoder.Encode(params)
	if err != nil {
		fn(UnsubscribeResult{}, err)
		return
	}

	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypeUnsubscribe,
		Params: paramsData,
	}
	c.sendAsync(cmd, func(r protocol.Reply, err error) {
		if err != nil {
			fn(UnsubscribeResult{}, err)
			return
		}
		if r.Error != nil {
			fn(UnsubscribeResult{}, r.Error)
			return
		}
		var res protocol.UnsubscribeResult
		err = c.resultDecoder.Decode(r.Result, &res)
		if err != nil {
			fn(UnsubscribeResult{}, err)
			return
		}
		fn(UnsubscribeResult{}, nil)
	})
}

func (c *Client) sendPing(fn func(error)) {
	cmd := &protocol.Command{
		ID:     c.nextMsgID(),
		Method: protocol.MethodTypePing,
	}
	c.sendAsync(cmd, func(_ protocol.Reply, err error) {
		fn(err)
	})
}

func (c *Client) sendAsync(cmd *protocol.Command, cb func(protocol.Reply, error)) {
	c.addRequest(cmd.ID, nil, cb)

	err := c.send(cmd)
	if err != nil {
		cb(protocol.Reply{}, err)
	}
	go func() {
		defer c.removeRequest(cmd.ID)
		select {
		case <-time.After(c.config.ReadTimeout):
			c.requestsMu.RLock()
			req, ok := c.requests[cmd.ID]
			c.requestsMu.RUnlock()
			if !ok {
				return
			}
			req.cb(protocol.Reply{}, ErrTimeout)
		}
	}()
}

func (c *Client) send(cmd *protocol.Command) error {
	transport := c.transport
	if transport == nil {
		return ErrClientDisconnected
	}
	err := transport.Write(cmd, c.config.WriteTimeout)
	if err != nil {
		go c.handleDisconnect(&disconnect{Reason: "write error", Reconnect: true})
		return io.EOF
	}
	return nil
}

type request struct {
	ch chan protocol.Reply
	cb func(protocol.Reply, error)
}

func (c *Client) addRequest(id uint32, ch chan protocol.Reply, cb func(protocol.Reply, error)) {
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	c.requests[id] = request{ch, cb}
}

func (c *Client) removeRequest(id uint32) {
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	delete(c.requests, id)
}
