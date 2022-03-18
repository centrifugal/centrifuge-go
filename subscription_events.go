package centrifuge

// SubscribeEvent is an event context passed
// to subscribe success callback.
type SubscribeEvent struct {
	Recovered bool
	Data      []byte
}

// SubscriptionErrorEvent is a subscribe error event context passed to
// event callback.
type SubscriptionErrorEvent struct {
	Error error
}

// UnsubscribeEvent is an event passed to unsubscribe event handler.
type UnsubscribeEvent struct{}

type SubscriptionFailEvent struct {
	Reason SubFailReason
}

// LeaveEvent has info about user who left channel.
type LeaveEvent struct {
	ClientInfo
}

// JoinEvent has info about user who joined channel.
type JoinEvent struct {
	ClientInfo
}

// PublicationEvent has info about received channel Publication.
type PublicationEvent struct {
	Publication
}

// PublicationHandler is a function to handle messages published in
// channels.
type PublicationHandler func(PublicationEvent)

// JoinHandler is a function to handle join messages.
type JoinHandler func(JoinEvent)

// LeaveHandler is a function to handle leave messages.
type LeaveHandler func(LeaveEvent)

// UnsubscribeHandler is a function to handle unsubscribe event.
type UnsubscribeHandler func(UnsubscribeEvent)

// SubscribeHandler is a function to handle subscribe success event.
type SubscribeHandler func(SubscribeEvent)

// SubscriptionErrorHandler is a function to handle subscribe error event.
type SubscriptionErrorHandler func(SubscriptionErrorEvent)

type SubscriptionFailHandler func(SubscriptionFailEvent)

// subscriptionEventHub contains callback functions that will be called when
// corresponding event happens with subscription to channel.
type subscriptionEventHub struct {
	onSubscribe   SubscribeHandler
	onError       SubscriptionErrorHandler
	onPublication PublicationHandler
	onJoin        JoinHandler
	onLeave       LeaveHandler
	onUnsubscribe UnsubscribeHandler
	onFail        SubscriptionFailHandler
}

// newSubscriptionEventHub initializes new subscriptionEventHub.
func newSubscriptionEventHub() *subscriptionEventHub {
	return &subscriptionEventHub{}
}

// OnSubscribe allows setting SubscribeHandler to SubEventHandler.
func (s *Subscription) OnSubscribe(handler SubscribeHandler) {
	s.events.onSubscribe = handler
}

// OnError allows setting SubscriptionErrorHandler to SubEventHandler.
func (s *Subscription) OnError(handler SubscriptionErrorHandler) {
	s.events.onError = handler
}

// OnPublication allows setting PublicationHandler to SubEventHandler.
func (s *Subscription) OnPublication(handler PublicationHandler) {
	s.events.onPublication = handler
}

// OnJoin allows setting JoinHandler to SubEventHandler.
func (s *Subscription) OnJoin(handler JoinHandler) {
	s.events.onJoin = handler
}

// OnLeave allows setting LeaveHandler to SubEventHandler.
func (s *Subscription) OnLeave(handler LeaveHandler) {
	s.events.onLeave = handler
}

// OnUnsubscribe allows setting UnsubscribeHandler to SubEventHandler.
func (s *Subscription) OnUnsubscribe(handler UnsubscribeHandler) {
	s.events.onUnsubscribe = handler
}

// OnFail ...
func (s *Subscription) OnFail(handler SubscriptionFailHandler) {
	s.events.onFail = handler
}
