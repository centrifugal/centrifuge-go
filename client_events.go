package centrifuge

// SubscriptionRefreshEvent contains info required to get subscription token when
// client wants to subscribe on private channel.
type SubscriptionRefreshEvent struct {
	ClientID string
	Channel  string
}

// ServerPublishEvent has info about received channel Publication.
type ServerPublishEvent struct {
	Channel string
	Publication
}

type ServerSubscribeEvent struct {
	Channel   string
	Recovered bool
	Data      []byte
}

// ServerJoinEvent has info about user who left channel.
type ServerJoinEvent struct {
	Channel string
	ClientInfo
}

// ServerLeaveEvent has info about user who joined channel.
type ServerLeaveEvent struct {
	Channel string
	ClientInfo
}

// ServerUnsubscribeEvent is an event passed to unsubscribe event handler.
type ServerUnsubscribeEvent struct {
	Channel string
}

// ConnectEvent is a connect event context passed to OnConnect callback.
type ConnectEvent struct {
	ClientID string
	Version  string
	Data     []byte
}

// DisconnectEvent is a disconnect event context passed to OnDisconnect callback.
type DisconnectEvent struct {
	Code      uint32
	Reason    string
	Reconnect bool
}

// ErrorEvent is an error event context passed to OnError callback.
type ErrorEvent struct {
	Error error
}

// FailEvent ...
type FailEvent struct {
	Reason FailReason
}

// MessageEvent is an event for async message from server to client.
type MessageEvent struct {
	Data []byte
}

// ConnectHandler is an interface describing how to handle connect event.
type ConnectHandler func(ConnectEvent)

// DisconnectHandler is an interface describing how to handle disconnect event.
type DisconnectHandler func(DisconnectEvent)

// MessageHandler is an interface describing how to handle async message from server.
type MessageHandler func(MessageEvent)

// ServerPublicationHandler is an interface describing how to handle Publication from
// server-side subscriptions.
type ServerPublicationHandler func(ServerPublishEvent)

// ServerSubscribeHandler is an interface describing how to handle subscribe events from
// server-side subscriptions.
type ServerSubscribeHandler func(ServerSubscribeEvent)

// ServerUnsubscribeHandler is an interface describing how to handle unsubscribe events from
// server-side subscriptions.
type ServerUnsubscribeHandler func(ServerUnsubscribeEvent)

// ServerJoinHandler is an interface describing how to handle Join events from
// server-side subscriptions.
type ServerJoinHandler func(ServerJoinEvent)

// ServerLeaveHandler is an interface describing how to handle Leave events from
// server-side subscriptions.
type ServerLeaveHandler func(ServerLeaveEvent)

// SubscriptionRefreshHandler is an interface describing how to handle private subscription request.
type SubscriptionRefreshHandler func(SubscriptionRefreshEvent) (string, error)

// RefreshHandler is an interface describing how to handle token refresh event.
type RefreshHandler func() (string, error)

// ErrorHandler is an interface describing how to handle error event.
type ErrorHandler func(ErrorEvent)

type FailHandler func(FailEvent)

// eventHub has all event handlers for client.
type eventHub struct {
	onConnect             ConnectHandler
	onDisconnect          DisconnectHandler
	onError               ErrorHandler
	onFail                FailHandler
	onMessage             MessageHandler
	onServerSubscribe     ServerSubscribeHandler
	onServerPublication   ServerPublicationHandler
	onServerJoin          ServerJoinHandler
	onServerLeave         ServerLeaveHandler
	onServerUnsubscribe   ServerUnsubscribeHandler
	onRefresh             RefreshHandler
	onSubscriptionRefresh SubscriptionRefreshHandler
}

// newEventHub initializes new eventHub.
func newEventHub() *eventHub {
	return &eventHub{}
}

// OnConnect is a function to handle connect event.
func (c *Client) OnConnect(handler ConnectHandler) {
	c.events.onConnect = handler
}

// OnError is a function that will receive unhandled errors for logging.
func (c *Client) OnError(handler ErrorHandler) {
	c.events.onError = handler
}

// OnDisconnect is a function to handle disconnect event.
func (c *Client) OnDisconnect(handler DisconnectHandler) {
	c.events.onDisconnect = handler
}

// OnFail ...
func (c *Client) OnFail(handler FailHandler) {
	c.events.onFail = handler
}

// OnRefresh handles refresh event when client's credentials expired and must be refreshed.
func (c *Client) OnRefresh(handler RefreshHandler) {
	c.events.onRefresh = handler
}

// OnSubscriptionRefresh needed to handle private channel subscriptions.
func (c *Client) OnSubscriptionRefresh(handler SubscriptionRefreshHandler) {
	c.events.onSubscriptionRefresh = handler
}

// OnMessage allows processing async message from server to client.
func (c *Client) OnMessage(handler MessageHandler) {
	c.events.onMessage = handler
}

// OnServerPublication sets function to handle Publications from server-side subscriptions.
func (c *Client) OnServerPublication(handler ServerPublicationHandler) {
	c.events.onServerPublication = handler
}

// OnServerSubscribe sets function to handle server-side subscription subscribe events.
func (c *Client) OnServerSubscribe(handler ServerSubscribeHandler) {
	c.events.onServerSubscribe = handler
}

// OnServerUnsubscribe sets function to handle unsubscribe from server-side subscriptions.
func (c *Client) OnServerUnsubscribe(handler ServerUnsubscribeHandler) {
	c.events.onServerUnsubscribe = handler
}

// OnServerJoin sets function to handle Join event from server-side subscriptions.
func (c *Client) OnServerJoin(handler ServerJoinHandler) {
	c.events.onServerJoin = handler
}

// OnServerLeave sets function to handle Leave event from server-side subscriptions.
func (c *Client) OnServerLeave(handler ServerLeaveHandler) {
	c.events.onServerLeave = handler
}
