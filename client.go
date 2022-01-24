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
	Code      uint32
	Reason    string
	Reconnect bool
}

// Describe client connection statuses.
const (
	DISCONNECTED = iota
	CONNECTING
	CONNECTED
	CLOSED
)

type serverSub struct {
	Offset      uint64
	Epoch       string
	Recoverable bool
}

// Client represents client connection to Centrifugo or Centrifuge
// library based server. It provides methods to set various event
// handlers, subscribe channels, call RPC commands etc. Call client
// Connect method to trigger actual connection with server. Call client
// Close method to clean up state when you don't need client instance
// anymore.
type Client struct {
	futureID            uint64
	msgID               uint32
	mu                  sync.RWMutex
	urls                []string
	round               int
	protocolType        protocol.Type
	config              Config
	token               string
	connectData         protocol.Raw
	transport           transport
	status              int
	id                  string
	subs                map[string]*Subscription
	serverSubs          map[string]*serverSub
	requestsMu          sync.RWMutex
	requests            map[uint32]request
	receive             chan []byte
	reconnect           bool
	reconnectAttempts   int
	reconnectStrategy   reconnectStrategy
	events              *eventHub
	paramsEncoder       protocol.ParamsEncoder
	resultDecoder       protocol.ResultDecoder
	commandEncoder      protocol.CommandEncoder
	pushEncoder         protocol.PushEncoder
	pushDecoder         protocol.PushDecoder
	delayPing           chan struct{}
	reconnectCh         chan struct{}
	closeCh             chan struct{}
	connectFutures      map[uint64]connectFuture
	hasBeenDisconnected bool
	cbQueue             *cbQueue
}

func (c *Client) nextMsgID() uint32 {
	return atomic.AddUint32(&c.msgID, 1)
}

// New initializes Client. After client initialized call its Connect method
// to trigger connection establishment with server.
// Deprecated: if you are using Centrifuge >= v0.18.0 or Centrifugo >= 3 then use NewJsonClient or NewProtobufClient.
//goland:noinspection GoUnusedExportedFunction
func New(connectionURL string, config Config) *Client {
	return newClient(connectionURL, strings.Contains(connectionURL, "format=protobuf"), config)
}

// NewJsonClient initializes Client which uses JSON-based protocol internally.
// After client initialized call its Connect method.
func NewJsonClient(connectionURL string, config Config) *Client {
	return newClient(connectionURL, false, config)
}

// NewProtobufClient initializes Client which uses Protobuf-based protocol internally.
// After client initialized call its Connect method.
func NewProtobufClient(connectionURL string, config Config) *Client {
	return newClient(connectionURL, true, config)
}

func newClient(connectionURL string, isProtobuf bool, config Config) *Client {
	// We support setting multiple urls to try then in round-robin fashion. But
	// for now this feature is not documented and used for internal tests. In most
	// cases there should be a single public server WS endpoint.
	urls := strings.Split(connectionURL, ",")
	if len(urls) == 0 {
		panic("connection url required")
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "ws") {
			panic(fmt.Sprintf("unsupported connection endpoint: %s", u))
		}
	}

	if config.ProtocolVersion == 0 {
		config.ProtocolVersion = ProtocolVersion1
	}

	protocolType := protocol.TypeJSON
	if isProtobuf {
		protocolType = protocol.TypeProtobuf
	}

	c := &Client{
		urls:              urls,
		config:            config,
		status:            DISCONNECTED,
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
		reconnectCh:       make(chan struct{}, 1),
		closeCh:           make(chan struct{}),
		events:            newEventHub(),
		reconnect:         true,
		connectFutures:    make(map[uint64]connectFuture),
	}

	// Create the async callback queue.
	c.cbQueue = &cbQueue{}
	c.cbQueue.cond = sync.NewCond(&c.cbQueue.mu)
	go c.cbQueue.dispatch()

	// Start reconnect management goroutine.
	go c.reconnectRoutine()

	return c
}

// SetToken allows setting connection token to let client
// authenticate itself on connect.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// SetConnectData allows setting data to send in connect command.
func (c *Client) SetConnectData(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectData = data
}

// SetHeader allows setting custom header to be sent in Upgrade HTTP request.
func (c *Client) SetHeader(key, value string) {
	if c.config.Header == nil {
		c.config.Header = http.Header{}
	}
	c.config.Header.Set(key, value)
}

func (c *Client) connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status == CONNECTED
}

func (c *Client) subscribed(channel string) bool {
	c.mu.RLock()
	_, ok := c.subs[channel]
	c.mu.RUnlock()
	return ok
}

// clientID returns unique ID of this connection which is set by server after connect.
// It only available after connection was established and authorized.
func (c *Client) clientID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
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
	cmd := &protocol.Command{}
	params := &protocol.SendRequest{
		Data: data,
	}
	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			return err
		}
		cmd.Params = paramsData
		cmd.Method = protocol.Command_SEND
	} else {
		cmd.Send = params
	}
	return c.send(cmd)
}

// RPCResult contains data returned from server as RPC result.
type RPCResult struct {
	Data []byte
}

// RPC allows making RPC – send data to server and wait for response.
// RPC handler must be registered on server.
func (c *Client) RPC(data []byte) (RPCResult, error) {
	return c.NamedRPC("", data)
}

// NamedRPC allows making RPC – send data to server ant wait for response.
// RPC handler must be registered on server.
// In contrast to RPC method it allows passing method name.
func (c *Client) NamedRPC(method string, data []byte) (RPCResult, error) {
	resCh := make(chan RPCResult, 1)
	errCh := make(chan error, 1)
	c.rpc(method, data, func(result RPCResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (c *Client) rpc(method string, data []byte, fn func(RPCResult, error)) {
	cmd := &protocol.Command{
		Id: c.nextMsgID(),
	}

	params := &protocol.RPCRequest{
		Data:   data,
		Method: method,
	}

	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(RPCResult{}, fmt.Errorf("encode error: %v", err))
			return
		}
		cmd.Params = paramsData
		cmd.Method = protocol.Command_RPC
	} else {
		cmd.Rpc = params
	}

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(RPCResult{}, err)
			return
		}
		if r.Error != nil {
			fn(RPCResult{}, errorFromProto(r.Error))
			return
		}
		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.RPCResult
			err = c.resultDecoder.Decode(r.Result, &res)
			if err != nil {
				fn(RPCResult{}, err)
				return
			}
			fn(RPCResult{Data: res.Data}, nil)
		} else {
			fn(RPCResult{Data: r.Rpc.Data}, nil)
		}
	})
	if err != nil {
		fn(RPCResult{}, err)
		return
	}
}

// Close closes Client forever and cleans up state.
func (c *Client) Close() error {
	err := c.Disconnect()
	c.mu.Lock()
	if c.status == CLOSED {
		c.mu.Unlock()
		return nil
	}
	c.cbQueue.close()
	close(c.closeCh)
	c.status = CLOSED
	c.mu.Unlock()
	return err
}

// reconnectRoutine manages re-connections to a server. It does this using
// reconnectStrategy which is exponential back-off by default. It also ensures
// that no more than one connection attempt happens concurrently.
func (c *Client) reconnectRoutine() {
	var semaphore chan struct{}
	for {
		select {
		case <-c.closeCh:
			return
		case _, ok := <-c.reconnectCh:
			if !ok {
				return
			}
			if semaphore != nil {
				<-semaphore
			}
			semaphore = make(chan struct{}, 1)
			c.mu.RLock()
			duration, err := c.reconnectStrategy.timeBeforeNextAttempt(c.reconnectAttempts)
			//fmt.Printf("Retry connection after %s, attempt %d\n", duration, c.reconnectAttempts)
			c.mu.RUnlock()
			if err != nil {
				c.handleError(err)
				return
			}
			select {
			case <-c.closeCh:
			case <-time.After(duration):
			}
			c.mu.Lock()
			if c.status != CONNECTING {
				c.mu.Unlock()
				semaphore <- struct{}{}
				continue
			}
			c.reconnectAttempts++
			if !c.reconnect {
				c.mu.Unlock()
				semaphore <- struct{}{}
				continue
			}
			c.mu.Unlock()
			err = c.connectFromScratch(true, func() {
				semaphore <- struct{}{}
			})
			if err != nil {
				c.handleError(err)
			}
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
	if c.status == DISCONNECTED || c.status == CLOSED {
		c.mu.Unlock()
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

	subsToUnsubscribe := make([]*Subscription, 0, len(c.subs))
	for _, s := range c.subs {
		subsToUnsubscribe = append(subsToUnsubscribe, s)
	}
	serverSubsToUnsubscribe := make([]string, 0, len(c.serverSubs))
	for ch := range c.serverSubs {
		serverSubsToUnsubscribe = append(serverSubsToUnsubscribe, ch)
	}

	needDisconnectEvent := !c.hasBeenDisconnected || c.status == CONNECTED
	c.reconnect = d.Reconnect
	if c.reconnect {
		c.status = CONNECTING
	} else {
		c.resolveConnectFutures(ErrClientDisconnected)
		c.status = DISCONNECTED
	}
	c.hasBeenDisconnected = true
	c.mu.Unlock()

	for _, req := range reqs {
		if req.cb != nil {
			req.cb(nil, ErrClientDisconnected)
		}
	}

	for _, s := range subsToUnsubscribe {
		s.triggerOnUnsubscribe(true, d.Reconnect)
	}

	var handler DisconnectHandler
	if c.events != nil && c.events.onDisconnect != nil {
		handler = c.events.onDisconnect
	}

	var serverUnsubscribeHandler ServerUnsubscribeHandler
	if c.events != nil && c.events.onServerUnsubscribe != nil {
		serverUnsubscribeHandler = c.events.onServerUnsubscribe
	}

	if handler != nil && needDisconnectEvent {
		c.runHandler(func() {
			if serverUnsubscribeHandler != nil {
				for _, ch := range serverSubsToUnsubscribe {
					serverUnsubscribeHandler.OnServerUnsubscribe(c, ServerUnsubscribeEvent{Channel: ch})
				}
			}
			event := DisconnectEvent{Reason: d.Reason, Reconnect: d.Reconnect}
			if c.config.ProtocolVersion >= ProtocolVersion2 {
				event.Code = d.Code
			}
			handler.OnDisconnect(c, event)
		})
	}

	if !d.Reconnect {
		return
	}

	select {
	case c.reconnectCh <- struct{}{}:
	default:
	}
}

func (c *Client) periodicPing(closeCh chan struct{}) {
	timeout := c.config.PingInterval
	for {
		select {
		case <-c.delayPing:
		case <-time.After(timeout):
			c.sendPing(func(err error) {
				if err != nil {
					go c.handleDisconnect(&disconnect{Code: 11, Reason: "no ping", Reconnect: true})
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
	c.cbQueue.push(fn)
}

func (c *Client) handle(reply *protocol.Reply) error {
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
		if c.config.ProtocolVersion == ProtocolVersion1 {
			push, err := c.pushDecoder.Decode(reply.Result)
			if err != nil {
				return err
			}
			err = c.handlePush(push)
			if err != nil {
				return err
			}
		} else {
			err := c.handlePushV2(reply.Push)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) handleMessage(msg *protocol.Message) error {
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

func (c *Client) handlePush(msg *protocol.Push) error {
	switch msg.Type {
	case protocol.Push_MESSAGE:
		m, err := c.pushDecoder.DecodeMessage(msg.Data)
		if err != nil {
			return err
		}
		_ = c.handleMessage(m)
	case protocol.Push_UNSUBSCRIBE:
		m, err := c.pushDecoder.DecodeUnsubscribe(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerUnsub(channel, m)
		}
		sub.handleUnsubscribe(m)
	case protocol.Push_PUBLICATION:
		m, err := c.pushDecoder.DecodePublication(msg.Data)
		if err != nil {
			return err
		}
		channel := msg.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerPublication(channel, m)
		}
		sub.handlePublication(m)
	case protocol.Push_JOIN:
		m, err := c.pushDecoder.DecodeJoin(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerJoin(channel, m)
		}
		sub.handleJoin(m.Info)
	case protocol.Push_LEAVE:
		m, err := c.pushDecoder.DecodeLeave(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerLeave(channel, m)
		}
		sub.handleLeave(m.Info)
	case protocol.Push_SUBSCRIBE:
		m, err := c.pushDecoder.DecodeSubscribe(msg.Data)
		if err != nil {
			return nil
		}
		channel := msg.Channel
		c.mu.RLock()
		_, ok := c.subs[channel]
		c.mu.RUnlock()
		if ok {
			// Client-side subscription exists.
			return nil
		}
		return c.handleServerSub(channel, m)
	default:
		return nil
	}
	return nil
}

func (c *Client) handlePushV2(push *protocol.Push) error {
	switch {
	case push.Message != nil:
		_ = c.handleMessage(push.Message)
	case push.Unsubscribe != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerUnsub(channel, push.Unsubscribe)
		}
		sub.handleUnsubscribe(push.Unsubscribe)
	case push.Pub != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerPublication(channel, push.Pub)
		}
		sub.handlePublication(push.Pub)
	case push.Join != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerJoin(channel, push.Join)
		}
		sub.handleJoin(push.Join.Info)
	case push.Leave != nil:
		channel := push.Channel
		c.mu.RLock()
		sub, ok := c.subs[channel]
		c.mu.RUnlock()
		if !ok {
			return c.handleServerLeave(channel, push.Leave)
		}
		sub.handleLeave(push.Leave.Info)
	case push.Subscribe != nil:
		channel := push.Channel
		c.mu.RLock()
		_, ok := c.subs[channel]
		c.mu.RUnlock()
		if ok {
			// Client-side subscription exists.
			return nil
		}
		return c.handleServerSub(channel, push.Subscribe)
	default:
		return nil
	}
	return nil
}

func (c *Client) handleServerPublication(channel string, pub *protocol.Publication) error {
	c.mu.RLock()
	_, ok := c.serverSubs[channel]
	c.mu.RUnlock()
	if !ok {
		return nil
	}

	var handler ServerPublishHandler
	if c.events != nil && c.events.onServerPublish != nil {
		handler = c.events.onServerPublish
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerPublish(c, ServerPublishEvent{Channel: channel, Publication: pubFromProto(pub)})
			c.mu.Lock()
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
	return nil
}

func (c *Client) handleServerJoin(channel string, join *protocol.Join) error {
	c.mu.RLock()
	_, ok := c.serverSubs[channel]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	var handler ServerJoinHandler
	if c.events != nil && c.events.onServerJoin != nil {
		handler = c.events.onServerJoin
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerJoin(c, ServerJoinEvent{Channel: channel, ClientInfo: infoFromProto(join.Info)})
		})
	}
	return nil
}

func (c *Client) handleServerLeave(channel string, leave *protocol.Leave) error {
	c.mu.RLock()
	_, ok := c.serverSubs[channel]
	c.mu.RUnlock()
	if !ok {
		return nil
	}

	var handler ServerLeaveHandler
	if c.events != nil && c.events.onServerLeave != nil {
		handler = c.events.onServerLeave
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerLeave(c, ServerLeaveEvent{Channel: channel, ClientInfo: infoFromProto(leave.Info)})
		})
	}
	return nil
}

func (c *Client) handleServerSub(channel string, sub *protocol.Subscribe) error {
	c.mu.Lock()
	_, ok := c.serverSubs[channel]
	if ok {
		return nil
	}
	c.serverSubs[channel] = &serverSub{
		Offset:      sub.Offset,
		Epoch:       sub.Epoch,
		Recoverable: sub.Recoverable,
	}
	c.mu.Unlock()
	if !ok {
		return nil
	}

	var handler ServerSubscribeHandler
	if c.events != nil && c.events.onServerSubscribe != nil {
		handler = c.events.onServerSubscribe
	}
	if handler != nil {
		c.runHandler(func() {
			handler.OnServerSubscribe(c, ServerSubscribeEvent{Channel: channel, Resubscribed: false, Recovered: false})
		})
	}
	return nil
}

func (c *Client) handleServerUnsub(channel string, _ *protocol.Unsubscribe) error {
	c.mu.Lock()
	_, ok := c.serverSubs[channel]
	if ok {
		delete(c.serverSubs, channel)
	}
	c.mu.Unlock()
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

// Connect dials to server and sends connect message. Will return an error if first dial
// with server failed. In case of failure client will automatically reconnect with
// exponential backoff.
func (c *Client) Connect() error {
	return c.connectFromScratch(false, func() {})
}

func (c *Client) connectFromScratch(isReconnect bool, reconnectWaitCB func()) error {
	c.mu.Lock()
	if isReconnect && c.status == DISCONNECTED {
		c.mu.Unlock()
		reconnectWaitCB()
		return nil
	}
	if c.status == CLOSED {
		c.mu.Unlock()
		reconnectWaitCB()
		return ErrClientClosed
	}
	c.status = CONNECTING
	c.reconnect = true
	c.round++
	round := c.round
	c.mu.Unlock()

	wsConfig := websocketConfig{
		NetDialContext:    c.config.NetDialContext,
		TLSConfig:         c.config.TLSConfig,
		HandshakeTimeout:  c.config.HandshakeTimeout,
		EnableCompression: c.config.EnableCompression,
		CookieJar:         c.config.CookieJar,
		Header:            c.config.Header,
	}

	u := c.urls[round%len(c.urls)]
	t, err := newWebsocketTransport(u, c.protocolType, wsConfig)
	if err != nil {
		go c.handleDisconnect(&disconnect{Code: 1, Reason: "connect error", Reconnect: true})
		reconnectWaitCB()
		return err
	}

	c.mu.Lock()
	if c.status == CONNECTED || c.status == DISCONNECTED || c.status == CLOSED {
		_ = t.Close()
		c.mu.Unlock()
		reconnectWaitCB()
		return nil
	}

	closeCh := make(chan struct{})
	c.receive = make(chan []byte, 64)
	c.transport = t
	go c.reader(t, closeCh)
	err = c.sendConnect(isReconnect, func(res *protocol.ConnectResult, err error) {
		defer reconnectWaitCB()
		c.mu.Lock()
		if c.status != CONNECTING {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		if err != nil {
			if isTokenExpiredError(err) {
				// Try to refresh token before next connection attempt.
				_ = c.refreshToken()
				c.mu.Lock()
				if c.status != CONNECTING {
					c.mu.Unlock()
					return
				}
				c.mu.Unlock()
			}
			go c.handleDisconnect(&disconnect{Code: 6, Reason: "connect error", Reconnect: true})
			return
		}

		c.mu.Lock()
		if c.status != CONNECTING {
			c.mu.Unlock()
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
			}(res.Ttl, closeCh)
		}
		c.resolveConnectFutures(nil)
		c.mu.Unlock()

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

		var subscribeHandler ServerSubscribeHandler
		if c.events != nil && c.events.onServerSubscribe != nil {
			subscribeHandler = c.events.onServerSubscribe
		}

		var publishHandler ServerPublishHandler
		if c.events != nil && c.events.onServerPublish != nil {
			publishHandler = c.events.onServerPublish
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
					subscribeHandler.OnServerSubscribe(c, ServerSubscribeEvent{
						Channel:      channel,
						Resubscribed: isReconnect, // TODO: check request map.
						Recovered:    subRes.Recovered,
					})
				})
			}
			if publishHandler != nil {
				c.runHandler(func() {
					for _, pub := range subRes.Publications {
						publishHandler.OnServerPublish(c, ServerPublishEvent{Channel: channel, Publication: pubFromProto(pub)})
						c.mu.Lock()
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

		if c.status != CONNECTED {
			return
		}

		err = c.resubscribe(c.id)
		if err != nil {
			// we need just to close the connection and outgoing requests here
			// but preserve all subscriptions.
			go c.handleDisconnect(&disconnect{Code: 8, Reason: "subscribe error", Reconnect: true})
			return
		}

		// Successfully connected – can reset reconnect attempts.
		c.reconnectAttempts = 0

		go c.periodicPing(closeCh)
	})
	c.mu.Unlock()
	if err != nil {
		reconnectWaitCB()
		go c.handleDisconnect(&disconnect{Code: 6, Reason: "connect error", Reconnect: true})
	}
	return err
}

func (c *Client) resubscribe(clientID string) error {
	for _, sub := range c.subs {
		err := sub.resubscribe(true, clientID, SubscribeOptions{})
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
	c.mu.Lock()
	c.reconnect = reconnect
	c.mu.Unlock()
	c.handleDisconnect(&disconnect{
		Code:      0,
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
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

func (c *Client) sendRefresh(closeCh chan struct{}) {
	err := c.refreshToken()
	if err != nil {
		return
	}

	c.mu.RLock()
	cmd := &protocol.Command{
		Id: c.nextMsgID(),
	}
	params := &protocol.RefreshRequest{
		Token: c.token,
	}
	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			c.mu.RUnlock()
			return
		}
		cmd.Method = protocol.Command_REFRESH
		cmd.Params = paramsData
	} else {
		cmd.Refresh = params
	}
	c.mu.RUnlock()

	_ = c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			return
		}
		if r.Error != nil {
			return
		}
		var expires bool
		var ttl uint32
		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.RefreshResult
			err = c.resultDecoder.Decode(r.Result, &res)
			if err != nil {
				return
			}
			expires = res.Expires
			ttl = res.Ttl
		} else {
			expires = r.Refresh.Expires
			ttl = r.Refresh.Ttl
		}
		if expires {
			go func(interval uint32) {
				select {
				case <-closeCh:
					return
				case <-time.After(time.Duration(interval) * time.Second):
					c.sendRefresh(closeCh)
				}
			}(ttl)
		}
	})
}

func (c *Client) sendSubRefresh(channel string, fn func(*protocol.SubRefreshResult, error)) {
	c.mu.RLock()
	clientID := c.id
	c.mu.RUnlock()

	token, err := c.privateSign(channel, clientID)
	if err != nil {
		return
	}

	c.mu.RLock()
	if c.id != clientID {
		c.mu.RUnlock()
		return
	}
	cmd := &protocol.Command{
		Id: c.nextMsgID(),
	}
	params := &protocol.SubRefreshRequest{
		Channel: channel,
		Token:   token,
	}
	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			c.mu.RUnlock()
			fn(nil, err)
			return
		}
		cmd.Method = protocol.Command_SUB_REFRESH
		cmd.Params = paramsData
	} else {
		cmd.SubRefresh = params
	}
	c.mu.RUnlock()

	_ = c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(nil, err)
			return
		}
		if r.Error != nil {
			fn(nil, errorFromProto(r.Error))
			return
		}
		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.SubRefreshResult
			err = c.resultDecoder.Decode(r.Result, &res)
			if err != nil {
				fn(nil, err)
				return
			}
			fn(&res, nil)
		} else {
			fn(r.SubRefresh, nil)
		}
	})
}

func (c *Client) sendConnect(isReconnect bool, fn func(*protocol.ConnectResult, error)) error {
	cmd := &protocol.Command{
		Id: c.nextMsgID(),
	}

	if c.token != "" || c.connectData != nil || len(c.serverSubs) > 0 || c.config.Name != "" || c.config.Version != "" {
		params := &protocol.ConnectRequest{}
		params.Token = c.token
		params.Name = c.config.Name
		params.Version = c.config.Version
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
		if c.config.ProtocolVersion == ProtocolVersion1 {
			paramsData, err := c.paramsEncoder.Encode(params)
			if err != nil {
				return err
			}
			cmd.Method = protocol.Command_CONNECT
			cmd.Params = paramsData
		} else {
			cmd.Connect = params
		}
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
		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.ConnectResult
			err = c.resultDecoder.Decode(reply.Result, &res)
			if err != nil {
				fn(nil, err)
				return
			}
			fn(&res, nil)
		} else {
			fn(reply.Connect, nil)
		}
	})
}

func (c *Client) privateSign(channel string, clientID string) (string, error) {
	var token string
	if strings.HasPrefix(channel, c.config.PrivateChannelPrefix) && c.events != nil {
		handler := c.events.onPrivateSub
		if handler != nil {
			ev := PrivateSubEvent{
				ClientID: clientID,
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
	sub = c.newSubscription(channel, config...)
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
		Id: c.nextMsgID(),
	}
	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			return err
		}
		cmd.Method = protocol.Command_SUBSCRIBE
		cmd.Params = paramsData
	} else {
		cmd.Subscribe = params
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
		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.SubscribeResult
			err = c.resultDecoder.Decode(reply.Result, &res)
			if err != nil {
				fn(nil, err)
				return
			}
			fn(&res, nil)
		} else {
			fn(reply.Subscribe, nil)
		}
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
	if c.status == CONNECTED {
		c.mu.Unlock()
		fn(nil)
	} else if c.status == DISCONNECTED {
		c.mu.Unlock()
		fn(ErrClientDisconnected)
	} else if c.status == CLOSED {
		c.mu.Unlock()
		fn(ErrClientClosed)
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
		Id: c.nextMsgID(),
	}
	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(PublishResult{}, err)
			return
		}
		cmd.Method = protocol.Command_PUBLISH
		cmd.Params = paramsData
	} else {
		cmd.Publish = params
	}
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
		Id: c.nextMsgID(),
	}

	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		cmd.Params = paramsData
		cmd.Method = protocol.Command_HISTORY
	} else {
		cmd.History = params
	}

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		if r.Error != nil {
			fn(HistoryResult{}, errorFromProto(r.Error))
			return
		}
		var publications []*protocol.Publication
		var offset uint64
		var epoch string
		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.HistoryResult
			err = c.resultDecoder.Decode(r.Result, &res)
			if err != nil {
				fn(HistoryResult{}, err)
				return
			}
			publications = res.Publications
			offset = res.Offset
			epoch = res.Epoch
		} else {
			publications = r.History.Publications
			offset = r.History.Offset
			epoch = r.History.Epoch
		}
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
	Presence map[string]ClientInfo
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
		Id: c.nextMsgID(),
	}

	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		cmd.Method = protocol.Command_PRESENCE
		cmd.Params = paramsData
	} else {
		cmd.Presence = params
	}

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

		if c.config.ProtocolVersion == ProtocolVersion1 {
			var res protocol.PresenceResult
			err = c.resultDecoder.Decode(r.Result, &res)
			if err != nil {
				fn(PresenceResult{}, err)
				return
			}
			for uid, info := range res.Presence {
				p[uid] = infoFromProto(info)
			}
		} else {
			for uid, info := range r.Presence.Presence {
				p[uid] = infoFromProto(info)
			}
		}
		fn(PresenceResult{Presence: p}, nil)
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
		Id: c.nextMsgID(),
	}

	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		cmd.Method = protocol.Command_PRESENCE_STATS
		cmd.Params = paramsData
	} else {
		cmd.PresenceStats = params
	}

	err := c.sendAsync(cmd, func(r *protocol.Reply, err error) {
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		if r.Error != nil {
			fn(PresenceStatsResult{}, errorFromProto(r.Error))
			return
		}
		if c.config.ProtocolVersion == ProtocolVersion1 {
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
		} else {
			fn(PresenceStatsResult{PresenceStats{
				NumClients: int(r.PresenceStats.NumClients),
				NumUsers:   int(r.PresenceStats.NumUsers),
			}}, nil)
		}
	})
	if err != nil {
		fn(PresenceStatsResult{}, err)
		return
	}
}

type UnsubscribeResult struct{}

func (c *Client) unsubscribe(channel string, fn func(UnsubscribeResult, error)) {
	if !c.subscribed(channel) {
		return
	}
	c.sendUnsubscribe(channel, fn)
}

func (c *Client) removeSubscription(channel string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, channel)
}

func (c *Client) sendUnsubscribe(channel string, fn func(UnsubscribeResult, error)) {
	params := &protocol.UnsubscribeRequest{
		Channel: channel,
	}

	cmd := &protocol.Command{
		Id: c.nextMsgID(),
	}

	if c.config.ProtocolVersion == ProtocolVersion1 {
		paramsData, err := c.paramsEncoder.Encode(params)
		if err != nil {
			fn(UnsubscribeResult{}, err)
			return
		}
		cmd.Method = protocol.Command_UNSUBSCRIBE
		cmd.Params = paramsData
	} else {
		cmd.Unsubscribe = params
	}

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

func (c *Client) sendPing(fn func(error)) {
	cmd := &protocol.Command{
		Id: c.nextMsgID(),
	}
	if c.config.ProtocolVersion == ProtocolVersion1 {
		cmd.Method = protocol.Command_PING
	}
	// Skip v2 branch since Id is enough for ping.
	_ = c.sendAsync(cmd, func(_ *protocol.Reply, err error) {
		fn(err)
	})
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
			req.cb(nil, ErrClientClosed)
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
