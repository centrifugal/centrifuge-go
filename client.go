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

// State of client connection.
type State string

// Describe client connection states.
const (
	StateDisconnected State = "disconnected"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateFailed       State = "failed"
)

// FailReason describes the reason of client failure.
type FailReason string

// Different Subscription close reasons.
const (
	FailReasonServer        FailReason = "server"
	FailReasonConnectFailed FailReason = "connect failed"
	FailReasonRefreshFailed FailReason = "refresh failed"
	FailReasonUnauthorized  FailReason = "unauthorized"
	FailReasonUnrecoverable FailReason = "unrecoverable"
)

type serverSub struct {
	Offset      uint64
	Epoch       string
	Recoverable bool
}

type disconnect struct {
	Code      uint32
	Reason    string
	Reconnect bool
}

// Client represents client connection to Centrifugo or Centrifuge
// library based server. It provides methods to set various event
// handlers, subscribe channels, call RPC commands etc. Call client
// Connect method to trigger actual connection with a server. Call client
// Close method to clean up state when you don't need client instance
// anymore.
type Client struct {
	futureID          uint64
	cmdID             uint32
	mu                sync.RWMutex
	endpoints         []string
	round             int
	protocolType      protocol.Type
	config            Config
	token             string
	data              protocol.Raw
	transport         transport
	state             State
	id                string
	subs              map[string]*Subscription
	serverSubs        map[string]*serverSub
	requestsMu        sync.RWMutex
	requests          map[uint32]request
	receive           chan []byte
	reconnectAttempts int
	reconnectStrategy reconnectStrategy
	events            *eventHub
	sendPong          bool
	paramsEncoder     protocol.ParamsEncoder
	resultDecoder     protocol.ResultDecoder
	commandEncoder    protocol.CommandEncoder
	pushEncoder       protocol.PushEncoder
	pushDecoder       protocol.PushDecoder
	delayPing         chan struct{}
	closeCh           chan struct{}
	connectFutures    map[uint64]connectFuture
	cbQueue           *cbQueue
	reconnectTimer    *time.Timer
	refreshTimer      *time.Timer
	refreshRequired   bool
}

func (c *Client) nextCmdID() uint32 {
	return atomic.AddUint32(&c.cmdID, 1)
}

// NewJsonClient initializes Client which uses JSON-based protocol internally.
// After client initialized call its Connect method.
func NewJsonClient(endpoint string, config Config) *Client {
	return newClient(endpoint, false, config)
}

// NewProtobufClient initializes Client which uses Protobuf-based protocol internally.
// After client initialized call its Connect method.
func NewProtobufClient(endpoint string, config Config) *Client {
	return newClient(endpoint, true, config)
}

func newClient(endpoint string, isProtobuf bool, config Config) *Client {
	if config.ReadTimeout == 0 {
		config.ReadTimeout = 5 * time.Second
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = time.Second
	}
	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = time.Second
	}
	if config.MaxServerPingDelay == 0 {
		config.MaxServerPingDelay = 10 * time.Second
	}
	if config.PrivateChannelPrefix == "" {
		config.PrivateChannelPrefix = "$"
	}
	if config.Header == nil {
		config.Header = http.Header{}
	}
	if config.Name == "" {
		config.Name = "go"
	}
	// We support setting multiple endpoints to try in round-robin fashion. But
	// for now this feature is not documented and used for internal tests. In most
	// cases there should be a single public server WS endpoint.
	endpoints := strings.Split(endpoint, ",")
	if len(endpoints) == 0 {
		panic("connection url required")
	}
	for _, e := range endpoints {
		if !strings.HasPrefix(e, "ws") {
			panic(fmt.Sprintf("unsupported connection endpoint: %s", e))
		}
	}

	protocolType := protocol.TypeJSON
	if isProtobuf {
		protocolType = protocol.TypeProtobuf
	}

	c := &Client{
		endpoints:         endpoints,
		config:            config,
		state:             StateDisconnected,
		protocolType:      protocolType,
		subs:              make(map[string]*Subscription),
		serverSubs:        make(map[string]*serverSub),
		requests:          make(map[uint32]request),
		reconnectStrategy: defaultBackoffReconnect,
		paramsEncoder:     newParamsEncoder(protocolType),
		resultDecoder:     newResultDecoder(protocolType),
		commandEncoder:    newCommandEncoder(protocolType),
		pushEncoder:       newPushEncoder(protocolType),
		pushDecoder:       newPushDecoder(protocolType),
		delayPing:         make(chan struct{}, 32),
		events:            newEventHub(),
		connectFutures:    make(map[uint64]connectFuture),
	}
	c.token = config.Token
	c.data = config.Data
	return c
}

func (c *Client) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Client) isConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state == StateConnected
}

func (c *Client) isSubscribed(channel string) bool {
	c.mu.RLock()
	_, ok := c.subs[channel]
	c.mu.RUnlock()
	return ok
}

// clientID returns unique ID of this connection which is set by server after connect.
// It's only available after connection was established and authorized.
func (c *Client) clientID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.id
}

// Send message to server without waiting for response.
// Message handler must be registered on server.
func (c *Client) Send(data []byte) error {
	errCh := make(chan error, 1)
	c.onConnect(func(err error) {
		if err != nil {
			errCh <- err
			return
		}
		cmd := &protocol.Command{}
		params := &protocol.SendRequest{
			Data: data,
		}
		cmd.Send = params
		errCh <- c.send(cmd)
	})
	return <-errCh
}

// RPCResult contains data returned from server as RPC result.
type RPCResult struct {
	Data []byte
}

// RPC allows making RPC – send data to server and wait for response.
// RPC handler must be registered on server.
func (c *Client) RPC(method string, data []byte) (RPCResult, error) {
	resCh := make(chan RPCResult, 1)
	errCh := make(chan error, 1)
	c.sendRPC(method, data, func(result RPCResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (c *Client) sendRPC(method string, data []byte, fn func(RPCResult, error)) {
	c.onConnect(func(err error) {
		if err != nil {
			fn(RPCResult{}, err)
			return
		}
		cmd := &protocol.Command{
			Id: c.nextCmdID(),
		}

		params := &protocol.RPCRequest{
			Data:   data,
			Method: method,
		}

		cmd.Rpc = params

		err = c.sendAsync(cmd, func(r *protocol.Reply, err error) {
			if err != nil {
				fn(RPCResult{}, err)
				return
			}
			if r.Error != nil {
				fn(RPCResult{}, errorFromProto(r.Error))
				return
			}
			fn(RPCResult{Data: r.Rpc.Data}, nil)
		})
		if err != nil {
			fn(RPCResult{}, err)
			return
		}
	})
}

// Lock must be held outside.
func (c *Client) disconnect(d *disconnect) {
	if c.state == StateDisconnected || c.state == StateFailed {
		return
	}

	c.clearConnectedState()

	needDisconnectEvent := c.state == StateConnected
	if d.Reconnect {
		c.state = StateConnecting
	} else {
		c.resolveConnectFutures(ErrClientDisconnected)
		c.state = StateDisconnected
	}

	var handler DisconnectHandler
	if c.events != nil && c.events.onDisconnect != nil {
		handler = c.events.onDisconnect
	}

	if needDisconnectEvent && handler != nil {
		c.runHandler(func() {
			event := DisconnectEvent{Code: d.Code, Reason: d.Reason, Reconnect: d.Reconnect}
			handler(event)
		})
	}

	if c.transport != nil {
		_ = c.transport.Close()
		c.transport = nil
	}

	if c.state != StateConnecting {
		if d.Code >= 3000 {
			c.fail(FailReasonServer, d.Code)
		}
		return
	}
	c.reconnectAttempts++
	reconnectDelay := c.getReconnectDelay()
	c.reconnectTimer = time.AfterFunc(reconnectDelay, func() {
		_ = c.startReconnecting()
	})
}

// Close closes Client. Use this method if you don't want to use client anymore,
// otherwise prefer Disconnect.
func (c *Client) Close() {
	c.Disconnect()
}

// Lock must be held outside.
func (c *Client) fail(reason FailReason, disconnectCode uint32) {
	if c.state == StateFailed {
		return
	}
	c.disconnect(&disconnect{
		Code:      disconnectCode,
		Reason:    "fail",
		Reconnect: false,
	})
	c.state = StateFailed
	if reason == FailReasonUnrecoverable {
		c.serverSubs = map[string]*serverSub{}
	}
	if c.events != nil && c.events.onFail != nil {
		handler := c.events.onFail
		if handler != nil {
			c.runHandler(func() {
				handler(FailEvent{Reason: reason})
			})
		}
	}
	c.cbQueue.close()
	c.cbQueue = nil
}

func (c *Client) handleError(err error) {
	var handler ErrorHandler
	if c.events != nil && c.events.onError != nil {
		handler = c.events.onError
	}
	if handler != nil {
		c.runHandler(func() {
			handler(ErrorEvent{Error: err})
		})
	}
}

// Lock must be held outside.
func (c *Client) clearConnectedState() {
	if c.reconnectTimer != nil {
		c.reconnectTimer.Stop()
		c.reconnectTimer = nil
	}
	if c.refreshTimer != nil {
		c.refreshTimer.Stop()
		c.refreshTimer = nil
	}

	if c.closeCh != nil {
		close(c.closeCh)
		c.closeCh = nil
	}

	c.requestsMu.Lock()
	reqs := make(map[uint32]request, len(c.requests))
	for uid, req := range c.requests {
		reqs[uid] = req
	}
	c.requests = make(map[uint32]request)
	c.requestsMu.Unlock()

	subsToUnsubscribe := make([]*Subscription, 0, len(c.subs))
	for _, s := range c.subs {
		subsToUnsubscribe = append(subsToUnsubscribe, s)
	}
	serverSubsToUnsubscribe := make([]string, 0, len(c.serverSubs))
	for ch := range c.serverSubs {
		serverSubsToUnsubscribe = append(serverSubsToUnsubscribe, ch)
	}

	for _, req := range reqs {
		if req.cb != nil {
			go req.cb(nil, ErrClientDisconnected)
		}
	}

	for _, s := range subsToUnsubscribe {
		s.mu.Lock()
		s.moveToUnsubscribed(true)
		s.mu.Unlock()
	}

	if c.state == StateConnected {
		var serverUnsubscribeHandler ServerUnsubscribeHandler
		if c.events != nil && c.events.onServerUnsubscribe != nil {
			serverUnsubscribeHandler = c.events.onServerUnsubscribe
		}
		if serverUnsubscribeHandler != nil {
			c.runHandler(func() {
				for _, ch := range serverSubsToUnsubscribe {
					serverUnsubscribeHandler(ServerUnsubscribeEvent{Channel: ch})
				}
			})
		}
	}
}

func (c *Client) handleDisconnect(d *disconnect) {
	if d == nil {
		d = &disconnect{
			Code:      4,
			Reason:    "connection closed",
			Reconnect: true,
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnect(d)
}

func (c *Client) waitServerPing(disconnectCh chan struct{}, pingInterval uint32) {
	timeout := c.config.MaxServerPingDelay + time.Duration(pingInterval)*time.Second
	for {
		select {
		case <-c.delayPing:
		case <-time.After(timeout):
			go c.handleDisconnect(&disconnect{Code: 11, Reason: "no ping", Reconnect: true})
		case <-disconnectCh:
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
	c.handle(reply)
	return nil
}

func (c *Client) reader(t transport, disconnectCh chan struct{}) {
	defer close(disconnectCh)
	for {
		err := c.readOnce(t)
		if err != nil {
			return
		}
	}
}

func (c *Client) runHandler(fn func()) {
	c.cbQueue.push(fn)
}

func (c *Client) handle(reply *protocol.Reply) {
	if reply.Id > 0 {
		c.requestsMu.RLock()
		req, ok := c.requests[reply.Id]
		c.requestsMu.RUnlock()
		if ok {
			if req.cb != nil {
				req.cb(reply, nil)
			}
		}
		c.removeRequest(reply.Id)
	} else {
		if reply.Push == nil {
			// Ping from server, send pong if needed.
			c.mu.RLock()
			sendPong := c.sendPong
			c.mu.RUnlock()
			if sendPong {
				cmd := &protocol.Command{}
				_ = c.send(cmd)
			}
			return
		}
		c.handlePush(reply.Push)
	}
}

func (c *Client) handleMessage(msg *protocol.Message) error {
	var handler MessageHandler
	if c.events != nil && c.events.onMessage != nil {
		handler = c.events.onMessage
	}
	if handler != nil {
		event := MessageEvent{Data: msg.Data}
		c.runHandler(func() {
			handler(event)
		})
	}
	return nil
}

func (c *Client) handlePush(push *protocol.Push) {
	switch {
	case push.Message != nil:
		_ = c.handleMessage(push.Message)
	case push.Unsubscribe != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			c.handleServerUnsub(channel, push.Unsubscribe)
			return
		}
		sub.handleUnsubscribe(push.Unsubscribe)
	case push.Pub != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			c.handleServerPublication(channel, push.Pub)
			return
		}
		sub.handlePublication(push.Pub)
	case push.Join != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			c.handleServerJoin(channel, push.Join)
			return
		}
		sub.handleJoin(push.Join.Info)
	case push.Leave != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			c.handleServerLeave(channel, push.Leave)
			return
		}
		sub.handleLeave(push.Leave.Info)
	case push.Subscribe != nil:
		channel := push.Channel
		c.mu.RLock()
		_, ok := c.subs[channel]
		c.mu.RUnlock()
		if ok {
			// Client-side subscription exists.
			return
		}
		c.handleServerSub(channel, push.Subscribe)
		return
	case push.Disconnect != nil:
		code := push.Disconnect.Code
		reconnect := code < 3500 || code >= 5000 || (code >= 4000 && code < 4500)
		c.disconnect(&disconnect{
			Code:      code,
			Reason:    push.Disconnect.Reason,
			Reconnect: reconnect,
		})
	default:
	}
}

func (c *Client) handleServerPublication(channel string, pub *protocol.Publication) {
	c.mu.RLock()
	_, ok := c.serverSubs[channel]
	id := c.id
	if id == "" {
		c.mu.RUnlock()
		return
	}
	c.mu.RUnlock()
	if !ok {
		return
	}
	var handler ServerPublicationHandler
	if c.events != nil && c.events.onServerPublication != nil {
		handler = c.events.onServerPublication
	}
	if handler != nil {
		c.runHandler(func() {
			handler(ServerPublicationEvent{Channel: channel, Publication: pubFromProto(pub)})
			c.mu.Lock()
			if c.id != id {
				c.mu.Unlock()
				return
			}
			serverSub, ok := c.serverSubs[channel]
			if !ok {
				c.mu.Unlock()
				return
			}
			if serverSub.Recoverable && pub.Offset > 0 {
				serverSub.Offset = pub.Offset
			}
			c.mu.Unlock()
		})
	}
}

func (c *Client) handleServerJoin(channel string, join *protocol.Join) {
	c.mu.RLock()
	_, ok := c.serverSubs[channel]
	c.mu.RUnlock()
	if !ok {
		return
	}
	var handler ServerJoinHandler
	if c.events != nil && c.events.onServerJoin != nil {
		handler = c.events.onServerJoin
	}
	if handler != nil {
		c.runHandler(func() {
			handler(ServerJoinEvent{Channel: channel, ClientInfo: infoFromProto(join.Info)})
		})
	}
}

func (c *Client) handleServerLeave(channel string, leave *protocol.Leave) {
	c.mu.RLock()
	_, ok := c.serverSubs[channel]
	c.mu.RUnlock()
	if !ok {
		return
	}

	var handler ServerLeaveHandler
	if c.events != nil && c.events.onServerLeave != nil {
		handler = c.events.onServerLeave
	}
	if handler != nil {
		c.runHandler(func() {
			handler(ServerLeaveEvent{Channel: channel, ClientInfo: infoFromProto(leave.Info)})
		})
	}
}

func (c *Client) handleServerSub(channel string, sub *protocol.Subscribe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.serverSubs[channel]
	if ok {
		return
	}
	c.serverSubs[channel] = &serverSub{
		Offset:      sub.Offset,
		Epoch:       sub.Epoch,
		Recoverable: sub.Recoverable,
	}

	var handler ServerSubscribeHandler
	if c.events != nil && c.events.onServerSubscribe != nil {
		handler = c.events.onServerSubscribe
	}
	if handler != nil {
		c.runHandler(func() {
			handler(ServerSubscribeEvent{Channel: channel})
		})
	}
}

func (c *Client) handleServerUnsub(channel string, _ *protocol.Unsubscribe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.serverSubs[channel]
	if ok {
		delete(c.serverSubs, channel)
	}
	if !ok {
		return
	}

	var handler ServerUnsubscribeHandler
	if c.events != nil && c.events.onServerUnsubscribe != nil {
		handler = c.events.onServerUnsubscribe
	}
	if handler != nil {
		c.runHandler(func() {
			handler(ServerUnsubscribeEvent{Channel: channel})
		})
	}
}

// Connect dials to server and sends connect message. Will return an error if first dial
// with server failed. In case of failure client will automatically reconnect with
// exponential backoff.
func (c *Client) Connect() error {
	return c.startConnecting()
}

func (c *Client) getReconnectDelay() time.Duration {
	return c.reconnectStrategy.timeBeforeNextAttempt(c.reconnectAttempts)
}

func (c *Client) startReconnecting() error {
	c.mu.Lock()
	c.round++
	round := c.round
	if c.state != StateConnecting {
		c.mu.Unlock()
		return nil
	}
	refreshRequired := c.refreshRequired
	c.mu.Unlock()

	wsConfig := websocketConfig{
		NetDialContext:    c.config.NetDialContext,
		TLSConfig:         c.config.TLSConfig,
		HandshakeTimeout:  c.config.HandshakeTimeout,
		EnableCompression: c.config.EnableCompression,
		CookieJar:         c.config.CookieJar,
		Header:            c.config.Header,
	}

	u := c.endpoints[round%len(c.endpoints)]
	t, err := newWebsocketTransport(u, c.protocolType, wsConfig)
	if err != nil {
		c.handleError(TransportError{err})
		c.mu.Lock()
		if c.state != StateConnecting {
			_ = t.Close()
			c.mu.Unlock()
			return nil
		}
		c.reconnectAttempts++
		reconnectDelay := c.getReconnectDelay()
		c.reconnectTimer = time.AfterFunc(reconnectDelay, func() {
			_ = c.startReconnecting()
		})
		c.mu.Unlock()
		return err
	}

	if refreshRequired {
		// Try to refresh token.
		token, err := c.refreshToken()
		if err != nil {
			c.handleError(RefreshError{err})
			c.mu.Lock()
			if c.state != StateConnecting {
				_ = t.Close()
				c.mu.Unlock()
				return nil
			}
			c.reconnectAttempts++
			reconnectDelay := c.getReconnectDelay()
			c.reconnectTimer = time.AfterFunc(reconnectDelay, func() {
				_ = c.startReconnecting()
			})
			c.mu.Unlock()
			return err
		} else {
			c.mu.Lock()
			if token == "" {
				c.fail(FailReasonUnauthorized, 7)
				c.mu.Unlock()
				return nil
			}
			c.token = token
			if c.state != StateConnecting {
				c.mu.Unlock()
				return nil
			}
			c.mu.Unlock()
		}
	}

	c.mu.Lock()
	if c.state != StateConnecting {
		_ = t.Close()
		c.mu.Unlock()
		return nil
	}
	c.refreshRequired = false
	disconnectCh := make(chan struct{})
	c.receive = make(chan []byte, 64)
	c.transport = t

	go c.reader(t, disconnectCh)

	err = c.sendConnect(func(res *protocol.ConnectResult, err error) {
		c.mu.Lock()
		if c.state != StateConnecting {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		if err != nil {
			c.handleError(ConnectError{err})
			if isTokenExpiredError(err) {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.state != StateConnecting {
					return
				}
				c.refreshRequired = true
				c.reconnectAttempts++
				reconnectDelay := c.getReconnectDelay()
				c.reconnectTimer = time.AfterFunc(reconnectDelay, func() {
					_ = c.startReconnecting()
				})
				return
			} else if isUnrecoverablePositionError(err) {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.state != StateConnecting {
					return
				}
				c.fail(FailReasonUnrecoverable, 8)
				return
			} else if isServerError(err) && !isTemporaryError(err) {
				c.mu.Lock()
				defer c.mu.Unlock()
				c.fail(FailReasonConnectFailed, 5)
				return
			} else {
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.state != StateConnecting {
					_ = t.Close()
					return
				}
				c.reconnectAttempts++
				reconnectDelay := c.getReconnectDelay()
				c.reconnectTimer = time.AfterFunc(reconnectDelay, func() {
					_ = c.startReconnecting()
				})
				return
			}
		}
		c.mu.Lock()
		if c.state != StateConnecting {
			_ = t.Close()
			c.mu.Unlock()
			return
		}
		c.id = res.Client
		c.state = StateConnected

		if res.Expires {
			c.refreshTimer = time.AfterFunc(time.Duration(res.Ttl)*time.Second, c.sendRefresh)
		}
		c.resolveConnectFutures(nil)
		c.mu.Unlock()

		if c.events != nil && c.events.onConnect != nil {
			handler := c.events.onConnect
			ev := ConnectEvent{
				ClientID: c.clientID(),
				Version:  res.Version,
				Data:     res.Data,
			}
			c.runHandler(func() {
				handler(ev)
			})
		}

		var subscribeHandler ServerSubscribeHandler
		if c.events != nil && c.events.onServerSubscribe != nil {
			subscribeHandler = c.events.onServerSubscribe
		}

		var publishHandler ServerPublicationHandler
		if c.events != nil && c.events.onServerPublication != nil {
			publishHandler = c.events.onServerPublication
		}

		for channel, subRes := range res.Subs {
			c.mu.Lock()
			sub, ok := c.serverSubs[channel]
			if ok {
				sub.Epoch = subRes.Epoch
				sub.Recoverable = subRes.Recoverable
			} else {
				sub = &serverSub{
					Epoch:       subRes.Epoch,
					Offset:      subRes.Offset,
					Recoverable: subRes.Recoverable,
				}
			}
			if len(subRes.Publications) == 0 {
				sub.Offset = subRes.Offset
			}
			c.serverSubs[channel] = sub
			c.mu.Unlock()

			if subscribeHandler != nil {
				c.runHandler(func() {
					subscribeHandler(ServerSubscribeEvent{
						Channel:   channel,
						Data:      subRes.GetData(),
						Recovered: subRes.GetRecovered(),
					})
				})
			}
			if publishHandler != nil {
				c.runHandler(func() {
					for _, pub := range subRes.Publications {
						publishHandler(ServerPublicationEvent{Channel: channel, Publication: pubFromProto(pub)})
						c.mu.Lock()
						if c.id != res.Client {
							c.mu.Unlock()
							return
						}
						if sub, ok := c.serverSubs[channel]; ok {
							sub.Offset = pub.Offset
						}
						c.serverSubs[channel] = sub
						c.mu.Unlock()
					}
				})
			}
		}

		c.mu.Lock()
		defer c.mu.Unlock()

		// Successfully connected – can reset reconnect attempts.
		c.reconnectAttempts = 0

		if c.state != StateConnected {
			return
		}

		err = c.resubscribe()
		if err != nil {
			// we need just to close the connection and outgoing requests here
			// but preserve all subscriptions.
			go c.handleDisconnect(&disconnect{Code: 8, Reason: "subscribe error", Reconnect: true})
			return
		}

		if res.Ping > 0 {
			c.sendPong = res.Pong
			go c.waitServerPing(disconnectCh, res.Ping)
		}
	})
	if err != nil {
		_ = t.Close()
		c.reconnectAttempts++
		reconnectDelay := c.getReconnectDelay()
		c.reconnectTimer = time.AfterFunc(reconnectDelay, func() {
			_ = c.startReconnecting()
		})
		c.mu.Unlock()
		c.handleError(ConnectError{err})
	}
	c.mu.Unlock()
	return err
}

func (c *Client) startConnecting() error {
	c.mu.Lock()
	if c.state == StateConnected || c.state == StateConnecting {
		c.mu.Unlock()
		return nil
	}
	if c.cbQueue == nil {
		// Create the async callback queue.
		c.cbQueue = &cbQueue{}
		c.cbQueue.cond = sync.NewCond(&c.cbQueue.mu)
		go c.cbQueue.dispatch()
	}
	if c.closeCh == nil {
		c.closeCh = make(chan struct{})
	}
	c.state = StateConnecting
	c.mu.Unlock()
	return c.startReconnecting()
}

func (c *Client) resubscribe() error {
	for _, sub := range c.subs {
		err := sub.resubscribe()
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

func isUnrecoverablePositionError(err error) bool {
	if e, ok := err.(*Error); ok && e.Code == 112 {
		return true
	}
	return false
}

func isServerError(err error) bool {
	if _, ok := err.(*Error); ok {
		return true
	}
	return false
}

func isTemporaryError(err error) bool {
	if e, ok := err.(*Error); ok && e.Temporary {
		return true
	}
	return false
}

// Disconnect client from server.
func (c *Client) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnect(&disconnect{
		Code:      0,
		Reason:    "client",
		Reconnect: false,
	})
}

func (c *Client) refreshToken() (string, error) {
	var handler ConnectionTokenHandler
	if c.events != nil && c.events.onConnectionToken != nil {
		handler = c.events.onConnectionToken
	}
	if handler == nil {
		return "", errors.New("ConnectionTokenHandler must be set to handle expired token")
	}
	return handler()
}

func (c *Client) sendRefresh() {
	token, err := c.refreshToken()
	if err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.handleRefreshError(err)
		return
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	params := &protocol.RefreshRequest{
		Token: c.token,
	}
	cmd.Refresh = params

	_ = c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.handleRefreshError(err)
			return
		}
		if r.Error != nil {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.state != StateConnected {
				return
			}
			c.handleError(RefreshError{err})
			if r.Error.Temporary {
				c.refreshTimer = time.AfterFunc(10*time.Second, c.sendRefresh)
			} else {
				c.fail(FailReasonRefreshFailed, 6)
			}
			return
		}
		expires := r.Refresh.Expires
		ttl := r.Refresh.Ttl
		if expires {
			c.mu.Lock()
			if c.state == StateConnected {
				c.refreshTimer = time.AfterFunc(time.Duration(ttl)*time.Second, c.sendRefresh)
			}
			c.mu.Unlock()
		}
	})
}

// Lock must be held outside.
func (c *Client) handleRefreshError(err error) {
	if c.state != StateConnected {
		return
	}
	c.handleError(RefreshError{err})
	c.refreshTimer = time.AfterFunc(10*time.Second, c.sendRefresh)
	return
}

func (c *Client) sendSubRefresh(channel string, token string, fn func(*protocol.SubRefreshResult, error)) {
	c.mu.RLock()
	clientID := c.id
	if c.id != clientID {
		c.mu.RUnlock()
		return
	}
	c.mu.RUnlock()

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	params := &protocol.SubRefreshRequest{
		Channel: channel,
		Token:   token,
	}
	cmd.SubRefresh = params

	_ = c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(nil, err)
			return
		}
		if r.Error != nil {
			fn(nil, errorFromProto(r.Error))
			return
		}
		fn(r.SubRefresh, nil)
	})
}

func (c *Client) sendConnect(fn func(*protocol.ConnectResult, error)) error {
	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}

	if c.token != "" || c.data != nil || len(c.serverSubs) > 0 || c.config.Name != "" || c.config.Version != "" {
		params := &protocol.ConnectRequest{}
		params.Token = c.token
		params.Name = c.config.Name
		params.Version = c.config.Version
		if c.data != nil {
			params.Data = c.data
		}
		if len(c.serverSubs) > 0 {
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
		cmd.Connect = params
	}

	return c.sendAsync(cmd, func(reply *protocol.Reply, err error) {
		if err != nil {
			fn(nil, err)
			return
		}
		if reply.Error != nil {
			fn(nil, errorFromProto(reply.Error))
			return
		}
		fn(reply.Connect, nil)
	})
}

// NewSubscription allocates new Subscription on a channel. As soon as Subscription
// successfully created Client keeps reference to it inside internal map registry to
// manage automatic resubscribe on reconnect. After creating Subscription call its
// Subscription.Subscribe method to actually start subscribing process. To temporarily
// unsubscribe call Subscription.Unsubscribe. If you ended up with Subscription then
// you can free resources by calling Subscription.Close method.
func (c *Client) NewSubscription(channel string, config ...SubscriptionConfig) (*Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sub *Subscription
	if _, ok := c.subs[channel]; ok {
		return nil, ErrDuplicateSubscription
	}
	sub = newSubscription(c, channel, config...)
	c.subs[channel] = sub
	return sub, nil
}

type StreamPosition struct {
	Offset uint64
	Epoch  string
}

func (c *Client) sendSubscribe(channel string, data []byte, recover bool, streamPos StreamPosition, token string, fn func(res *protocol.SubscribeResult, err error)) error {
	params := &protocol.SubscribeRequest{
		Channel: channel,
	}

	if recover {
		params.Recover = true
		if streamPos.Offset > 0 {
			params.Offset = streamPos.Offset
		}
		params.Epoch = streamPos.Epoch
	}
	if token != "" {
		params.Token = token
	}
	params.Data = data

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	cmd.Subscribe = params

	return c.sendAsync(cmd, func(reply *protocol.Reply, err error) {
		if err != nil {
			fn(nil, err)
			return
		}
		if reply.Error != nil {
			fn(nil, errorFromProto(reply.Error))
			return
		}
		fn(reply.Subscribe, nil)
	})
}

func (c *Client) nextFutureID() uint64 {
	return atomic.AddUint64(&c.futureID, 1)
}

type connectFuture struct {
	fn      func(error)
	closeCh chan struct{}
}

func newConnectFuture(fn func(error)) connectFuture {
	return connectFuture{fn: fn, closeCh: make(chan struct{})}
}

// lock must be held outside.
func (c *Client) resolveConnectFutures(err error) {
	for _, fut := range c.connectFutures {
		fut.fn(err)
		close(fut.closeCh)
	}
	c.connectFutures = make(map[uint64]connectFuture)
}

func (c *Client) onConnect(fn func(err error)) {
	c.mu.Lock()
	if c.state == StateConnected {
		c.mu.Unlock()
		fn(nil)
	} else if c.state == StateDisconnected {
		c.mu.Unlock()
		fn(ErrClientDisconnected)
	} else if c.state == StateFailed {
		c.mu.Unlock()
		fn(ErrClientFailed)
	} else {
		defer c.mu.Unlock()
		id := c.nextFutureID()
		fut := newConnectFuture(fn)
		c.connectFutures[id] = fut
		go func() {
			select {
			case <-fut.closeCh:
			case <-time.After(c.config.ReadTimeout):
				c.mu.Lock()
				defer c.mu.Unlock()
				fut, ok := c.connectFutures[id]
				if !ok {
					return
				}
				delete(c.connectFutures, id)
				fut.fn(ErrTimeout)
			}
		}()
	}
}

// PublishResult contains the result of publish.
type PublishResult struct{}

// Publish data into channel.
func (c *Client) Publish(channel string, data []byte) (PublishResult, error) {
	resCh := make(chan PublishResult, 1)
	errCh := make(chan error, 1)
	c.publish(channel, data, func(result PublishResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (c *Client) publish(channel string, data []byte, fn func(PublishResult, error)) {
	c.onConnect(func(err error) {
		if err != nil {
			fn(PublishResult{}, err)
			return
		}
		c.sendPublish(channel, data, fn)
	})
}

func (c *Client) sendPublish(channel string, data []byte, fn func(PublishResult, error)) {
	params := &protocol.PublishRequest{
		Channel: channel,
		Data:    protocol.Raw(data),
	}

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	cmd.Publish = params
	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(PublishResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PublishResult{}, errorFromProto(r.Error))
			return
		}
		fn(PublishResult{}, nil)
	})
	if err != nil {
		fn(PublishResult{}, err)
	}
}

// HistoryResult contains the result of history op.
type HistoryResult struct {
	Publications []Publication
	Offset       uint64
	Epoch        string
}

// History for a channel without being subscribed.
func (c *Client) History(channel string, opts ...HistoryOption) (HistoryResult, error) {
	resCh := make(chan HistoryResult, 1)
	errCh := make(chan error, 1)
	historyOpts := &HistoryOptions{}
	for _, opt := range opts {
		opt(historyOpts)
	}
	c.history(channel, *historyOpts, func(result HistoryResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (c *Client) history(channel string, opts HistoryOptions, fn func(HistoryResult, error)) {
	c.onConnect(func(err error) {
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		c.sendHistory(channel, opts, fn)
	})
}

func (c *Client) sendHistory(channel string, opts HistoryOptions, fn func(HistoryResult, error)) {
	params := &protocol.HistoryRequest{
		Channel: channel,
		Limit:   opts.Limit,
		Reverse: opts.Reverse,
	}
	if opts.Since != nil {
		params.Since = &protocol.StreamPosition{
			Offset: opts.Since.Offset,
			Epoch:  opts.Since.Epoch,
		}
	}

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	cmd.History = params

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		if r.Error != nil {
			fn(HistoryResult{}, errorFromProto(r.Error))
			return
		}

		publications := r.History.Publications
		offset := r.History.Offset
		epoch := r.History.Epoch

		pubs := make([]Publication, len(publications))
		for i, m := range publications {
			pubs[i] = pubFromProto(m)
		}
		fn(HistoryResult{
			Publications: pubs,
			Offset:       offset,
			Epoch:        epoch,
		}, nil)
	})
	if err != nil {
		fn(HistoryResult{}, err)
		return
	}
}

// PresenceResult contains the result of presence op.
type PresenceResult struct {
	Clients map[string]ClientInfo
}

// Presence for a channel without being subscribed.
func (c *Client) Presence(channel string) (PresenceResult, error) {
	resCh := make(chan PresenceResult, 1)
	errCh := make(chan error, 1)
	c.presence(channel, func(result PresenceResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (c *Client) presence(channel string, fn func(PresenceResult, error)) {
	c.onConnect(func(err error) {
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		c.sendPresence(channel, fn)
	})
}

func (c *Client) sendPresence(channel string, fn func(PresenceResult, error)) {
	params := &protocol.PresenceRequest{
		Channel: channel,
	}

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	cmd.Presence = params

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PresenceResult{}, errorFromProto(r.Error))
			return
		}

		p := make(map[string]ClientInfo)

		for uid, info := range r.Presence.Presence {
			p[uid] = infoFromProto(info)
		}
		fn(PresenceResult{Clients: p}, nil)
	})
	if err != nil {
		fn(PresenceResult{}, err)
	}
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

// PresenceStats for a channel without being subscribed.
func (c *Client) PresenceStats(channel string) (PresenceStatsResult, error) {
	resCh := make(chan PresenceStatsResult, 1)
	errCh := make(chan error, 1)
	c.presenceStats(channel, func(result PresenceStatsResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (c *Client) presenceStats(channel string, fn func(PresenceStatsResult, error)) {
	c.onConnect(func(err error) {
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		c.sendPresenceStats(channel, fn)
	})
}

func (c *Client) sendPresenceStats(channel string, fn func(PresenceStatsResult, error)) {
	params := &protocol.PresenceStatsRequest{
		Channel: channel,
	}

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	cmd.PresenceStats = params

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PresenceStatsResult{}, errorFromProto(r.Error))
			return
		}
		fn(PresenceStatsResult{PresenceStats{
			NumClients: int(r.PresenceStats.NumClients),
			NumUsers:   int(r.PresenceStats.NumUsers),
		}}, nil)
	})
	if err != nil {
		fn(PresenceStatsResult{}, err)
		return
	}
}

type UnsubscribeResult struct{}

func (c *Client) unsubscribe(channel string, fn func(UnsubscribeResult, error)) {
	if !c.isSubscribed(channel) {
		return
	}
	c.sendUnsubscribe(channel, fn)
}

func (c *Client) sendUnsubscribe(channel string, fn func(UnsubscribeResult, error)) {
	params := &protocol.UnsubscribeRequest{
		Channel: channel,
	}

	cmd := &protocol.Command{
		Id: c.nextCmdID(),
	}
	cmd.Unsubscribe = params

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(UnsubscribeResult{}, err)
			return
		}
		if r.Error != nil {
			fn(UnsubscribeResult{}, errorFromProto(r.Error))
			return
		}
		fn(UnsubscribeResult{}, nil)
	})
	if err != nil {
		fn(UnsubscribeResult{}, err)
	}
}

func (c *Client) sendAsync(cmd *protocol.Command, cb func(*protocol.Reply, error)) error {
	c.addRequest(cmd.Id, cb)

	err := c.send(cmd)
	if err != nil {
		return err
	}
	go func() {
		defer c.removeRequest(cmd.Id)
		select {
		case <-time.After(c.config.ReadTimeout):
			c.requestsMu.RLock()
			req, ok := c.requests[cmd.Id]
			c.requestsMu.RUnlock()
			if !ok {
				return
			}
			req.cb(nil, ErrTimeout)
		case <-c.closeCh:
			c.requestsMu.RLock()
			req, ok := c.requests[cmd.Id]
			c.requestsMu.RUnlock()
			if !ok {
				return
			}
			req.cb(nil, ErrClientDisconnected)
		}
	}()
	return nil
}

func (c *Client) send(cmd *protocol.Command) error {
	transport := c.transport
	if transport == nil {
		return ErrClientDisconnected
	}
	err := transport.Write(cmd, c.config.WriteTimeout)
	if err != nil {
		go c.handleDisconnect(&disconnect{Code: 2, Reason: "write error", Reconnect: true})
		return io.EOF
	}
	return nil
}

type request struct {
	cb func(*protocol.Reply, error)
}

func (c *Client) addRequest(id uint32, cb func(*protocol.Reply, error)) {
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	c.requests[id] = request{cb}
}

func (c *Client) removeRequest(id uint32) {
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	delete(c.requests, id)
}
