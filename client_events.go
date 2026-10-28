package centrifuge

// ConnectionTokenEvent may contain some useful contextual information in the future.
// For now, it's empty.
type ConnectionTokenEvent struct {
}

// SubscriptionTokenEvent contains info required to get subscription token when
// client wants to subscribe on private channel.
type SubscriptionTokenEvent struct {
	Channel string
}

// SubscriptionGetStateEvent is passed to SubscriptionConfig.GetState callback.
type SubscriptionGetStateEvent struct {
	Channel string
}

// ServerPublicationEvent has info about received channel Publication.
type ServerPublicationEvent struct {
	Channel string
	Publication
}

// ServerSubscribedEvent has info about server-side subscription.
type ServerSubscribedEvent struct {
	Channel        string
	WasRecovering  bool
	Recovered      bool
	Recoverable    bool
	Positioned     bool
	StreamPosition *StreamPosition
	Data           []byte
}

// ServerJoinEvent has info about user who joined channel.
type ServerJoinEvent struct {
	Channel string
	ClientInfo
}

// ServerLeaveEvent has info about user who left channel.
type ServerLeaveEvent struct {
	Channel string
	ClientInfo
}

// ServerUnsubscribedEvent is an event passed to unsubscribe event handler.
type ServerUnsubscribedEvent struct {
	Channel string
}

// ServerSubscribingEvent is an event passed to subscribing event handler.
type ServerSubscribingEvent struct {
	Channel string
}

// ConnectedEvent is a connected event context passed to OnConnected callback.
type ConnectedEvent struct {
	ClientID string
	Version  string
	Data     []byte
}

// ConnectingEvent is a connecting event context passed to OnConnecting callback.
type ConnectingEvent struct {
	Code   uint32
	Reason string
}

// DisconnectedEvent is a disconnected event context passed to OnDisconnected callback.
type DisconnectedEvent struct {
	Code   uint32
	Reason string
}

// ErrorEvent is an error event context passed to OnError callback.
type ErrorEvent struct {
	Error error
}

// MessageEvent is an event for async message from server to client.
type MessageEvent struct {
	Data []byte
}

// ConnectingHandler is an interface describing how to handle connecting event.
type ConnectingHandler func(ConnectingEvent)

// ConnectedHandler is an interface describing how to handle connect event.
type ConnectedHandler func(ConnectedEvent)

// DisconnectHandler is an interface describing how to handle moveToDisconnected event.
type DisconnectHandler func(DisconnectedEvent)

// MessageHandler is an interface describing how to handle async message from server.
type MessageHandler func(MessageEvent)

// ServerPublicationHandler is an interface describing how to handle Publication from
// server-side subscriptions.
type ServerPublicationHandler func(ServerPublicationEvent)

// ServerSubscribedHandler is an interface describing how to handle subscribe events from
// server-side subscriptions.
type ServerSubscribedHandler func(ServerSubscribedEvent)

// ServerSubscribingHandler is an interface describing how to handle subscribing events for
// server-side subscriptions.
type ServerSubscribingHandler func(ServerSubscribingEvent)

// ServerUnsubscribedHandler is an interface describing how to handle unsubscribe events from
// server-side subscriptions.
type ServerUnsubscribedHandler func(ServerUnsubscribedEvent)

// ServerJoinHandler is an interface describing how to handle Join events from
// server-side subscriptions.
type ServerJoinHandler func(ServerJoinEvent)

// ServerLeaveHandler is an interface describing how to handle Leave events from
// server-side subscriptions.
type ServerLeaveHandler func(ServerLeaveEvent)

// ErrorHandler is an interface describing how to handle error event.
type ErrorHandler func(ErrorEvent)

// eventHub has all event handlers for client.
type eventHub struct {
	onConnected          ConnectedHandler
	onDisconnected       DisconnectHandler
	onConnecting         ConnectingHandler
	onError              ErrorHandler
	onMessage            MessageHandler
	onServerSubscribe    ServerSubscribedHandler
	onServerSubscribing  ServerSubscribingHandler
	onServerUnsubscribed ServerUnsubscribedHandler
	onServerPublication  ServerPublicationHandler
	onServerJoin         ServerJoinHandler
	onServerLeave        ServerLeaveHandler
}

// newEventHub initializes new eventHub.
func newEventHub() *eventHub {
	return &eventHub{}
}

// OnConnected is a function to handle connect event.
func (c *Client) OnConnected(handler ConnectedHandler) {
	c.events.onConnected = handler
}

// OnConnecting is a function to handle connecting event.
func (c *Client) OnConnecting(handler ConnectingHandler) {
	c.events.onConnecting = handler
}

// OnDisconnected is a function to handle moveToDisconnected event.
func (c *Client) OnDisconnected(handler DisconnectHandler) {
	c.events.onDisconnected = handler
}

// OnError is a function that will receive unhandled errors for logging.
func (c *Client) OnError(handler ErrorHandler) {
	c.events.onError = handler
}

// OnMessage allows processing async message from server to client.
func (c *Client) OnMessage(handler MessageHandler) {
	c.events.onMessage = handler
}

// OnPublication sets function to handle Publications from server-side subscriptions.
func (c *Client) OnPublication(handler ServerPublicationHandler) {
	c.events.onServerPublication = handler
}

// OnSubscribed sets function to handle server-side subscription subscribe events.
func (c *Client) OnSubscribed(handler ServerSubscribedHandler) {
	c.events.onServerSubscribe = handler
}

// OnSubscribing sets function to handle server-side subscription subscribing events.
func (c *Client) OnSubscribing(handler ServerSubscribingHandler) {
	c.events.onServerSubscribing = handler
}

// OnUnsubscribed sets function to handle unsubscribe from server-side subscriptions.
func (c *Client) OnUnsubscribed(handler ServerUnsubscribedHandler) {
	c.events.onServerUnsubscribed = handler
}

// OnJoin sets function to handle Join event from server-side subscriptions.
func (c *Client) OnJoin(handler ServerJoinHandler) {
	c.events.onServerJoin = handler
}

// OnLeave sets function to handle Leave event from server-side subscriptions.
func (c *Client) OnLeave(handler ServerLeaveHandler) {
	c.events.onServerLeave = handler
}

// DictionaryCompressionStats reports what a connection measured on the frames
// it decoded against a dictionary.
//
// The byte counts are exact at the protocol frame level: BytesReceived is what
// arrived in compressed WebSocket messages, BytesDecompressed is what those
// frames expanded to. Two caveats when reading them as a network saving:
//
//   - They compare against sending the same frames uncompressed, not against
//     permessage-deflate, which is the alternative most deployments would
//     otherwise be using.
//   - They are measured on WebSocket message payloads, so they exclude WebSocket
//     and TCP framing. Compression shrinks those slightly too, which makes the
//     figure a small underestimate of bytes actually taken off the wire.
//
// Frames received before compression was activated are not counted at all, so
// this reports the steady state rather than the whole connection.
type DictionaryCompressionStats struct {
	// Accepted reports whether the server enabled dictionary compression for this
	// connection. The SDK always advertises support and the server decides, so
	// this is how to tell whether it was taken up - and it is true as soon as the
	// connect reply is read, including inside an OnConnected handler.
	Accepted bool
	// Active reports whether the connection is currently decoding compressed
	// frames. It lags Accepted by one frame: the connect reply that carries the
	// dictionary is itself uncompressed, so decoding starts with the frame after
	// it.
	Active bool
	// Frames is how many compressed frames were received.
	Frames int64
	// BytesReceived is the compressed size of those frames.
	BytesReceived int64
	// BytesDecompressed is what they expanded to.
	BytesDecompressed int64
	// DictionaryID names the dictionary in use. It is a hash of the dictionary
	// content, so the same id always means the same bytes - which is what makes
	// it safe to cache one across connections.
	DictionaryID string

	// DictionaryBytes is what the dictionary itself cost to receive. It is a real
	// cost of the feature, so BytesSaved subtracts it.
	DictionaryBytes int64
}

// BytesSaved is the net saving: what these frames would have cost uncompressed,
// less what they actually cost, less the dictionary transfer. It can be negative
// on a connection that received too little traffic to earn the dictionary back.
func (s DictionaryCompressionStats) BytesSaved() int64 {
	return s.BytesDecompressed - s.BytesReceived - s.DictionaryBytes
}

// Ratio is uncompressed over compressed for the frames received, ignoring the
// dictionary transfer. Zero when nothing was received.
func (s DictionaryCompressionStats) Ratio() float64 {
	if s.BytesReceived == 0 {
		return 0
	}
	return float64(s.BytesDecompressed) / float64(s.BytesReceived)
}
