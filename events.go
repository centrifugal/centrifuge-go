package centrifuge

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
	Code      uint32
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

// MessageHandler is an interface describing how to async message from server.
type MessageHandler interface {
	OnMessage(*Client, MessageEvent)
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

// EventHub has all event handlers for client.
type EventHub struct {
	onConnect    ConnectHandler
	onDisconnect DisconnectHandler
	onPrivateSub PrivateSubHandler
	onRefresh    RefreshHandler
	onError      ErrorHandler
	onMessage    MessageHandler
}

// newEventHub initializes new EventHub.
func newEventHub() *EventHub {
	return &EventHub{}
}

// OnConnect is a function to handle connect event.
func (c *Client) OnConnect(handler ConnectHandler) {
	c.events.onConnect = handler
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
