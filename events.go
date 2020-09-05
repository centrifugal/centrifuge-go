package centrifuge

// PrivateSubEvent contains info required to create PrivateSign when client
// wants to subscribe on private channel.
type PrivateSubEvent struct {
	ClientID string
	Channel  string
}

// ServerPublishEvent has info about received channel Publication.
type ServerPublishEvent struct {
	Channel string
	Publication
}

type ServerSubscribeEvent struct {
	Channel      string
	Resubscribed bool
	Recovered    bool
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
	Reason    string
	Reconnect bool
}

// ErrorEvent is an error event context passed to OnError callback.
type ErrorEvent struct {
	// TODO: return error type here instead of string
	// so user code could distinguish various types of possible errors?
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

// MessageHandler is an interface describing how to handle async message from server.
type MessageHandler interface {
	OnMessage(*Client, MessageEvent)
}

// ServerPublishHandler is an interface describing how to handle Publication from
// server-side subscriptions.
type ServerPublishHandler interface {
	OnServerPublish(*Client, ServerPublishEvent)
}

// ServerSubscribeHandler is an interface describing how to handle subscribe events from
// server-side subscriptions.
type ServerSubscribeHandler interface {
	OnServerSubscribe(*Client, ServerSubscribeEvent)
}

// ServerUnsubscribeHandler is an interface describing how to handle unsubscribe events from
// server-side subscriptions.
type ServerUnsubscribeHandler interface {
	OnServerUnsubscribe(*Client, ServerUnsubscribeEvent)
}

// ServerJoinHandler is an interface describing how to handle Join events from
// server-side subscriptions.
type ServerJoinHandler interface {
	OnServerJoin(*Client, ServerJoinEvent)
}

// ServerLeaveHandler is an interface describing how to handle Leave events from
// server-side subscriptions.
type ServerLeaveHandler interface {
	OnServerLeave(*Client, ServerLeaveEvent)
}

// PrivateSubHandler is an interface describing how to handle private subscription request.
type PrivateSubHandler interface {
	OnPrivateSub(*Client, PrivateSubEvent) (string, error)
}

// RefreshHandler is an interface describing how to handle token refresh event.
type RefreshHandler interface {
	OnRefresh(*Client) (string, error)
}

// ErrorHandler is an interface describing how to handle error event.
type ErrorHandler interface {
	OnError(*Client, ErrorEvent)
}

// eventHub has all event handlers for client.
type eventHub struct {
	onConnect           ConnectHandler
	onDisconnect        DisconnectHandler
	onPrivateSub        PrivateSubHandler
	onRefresh           RefreshHandler
	onError             ErrorHandler
	onMessage           MessageHandler
	onServerSubscribe   ServerSubscribeHandler
	onServerPublish     ServerPublishHandler
	onServerJoin        ServerJoinHandler
	onServerLeave       ServerLeaveHandler
	onServerUnsubscribe ServerUnsubscribeHandler
}

// newEventHub initializes new eventHub.
func newEventHub() *eventHub {
	return &eventHub{}
}

// OnConnect is a function to handle connect event.
func (c *Client) OnConnect(handler ConnectHandler) {
	c.events.onConnect = handler
}

// OnServerPublish sets function to handle Publications from server-side subscriptions.
func (c *Client) OnServerPublish(handler ServerPublishHandler) {
	c.events.onServerPublish = handler
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

// OnDisconnect is a function to handle disconnect event.
func (c *Client) OnDisconnect(handler DisconnectHandler) {
	c.events.onDisconnect = handler
}

// OnPrivateSub needed to handle private channel subscriptions.
func (c *Client) OnPrivateSub(handler PrivateSubHandler) {
	c.events.onPrivateSub = handler
}

// OnRefresh handles refresh event when client's credentials expired and must be refreshed.
func (c *Client) OnRefresh(handler RefreshHandler) {
	c.events.onRefresh = handler
}

// OnError is a function that will receive unhandled errors for logging.
func (c *Client) OnError(handler ErrorHandler) {
	c.events.onError = handler
}

// OnMessage allows to process async message from server to client.
func (c *Client) OnMessage(handler MessageHandler) {
	c.events.onMessage = handler
}
