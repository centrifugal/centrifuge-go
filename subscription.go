package centrifuge

import (
	"fmt"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge-go/internal/proto"
)

// SubscribeSuccessEvent is a subscribe success event context passed
// to event callback.
type SubscribeSuccessEvent struct {
	Resubscribed bool
	Recovered    bool
}

// SubscribeErrorEvent is a subscribe error event context passed to
// event callback.
type SubscribeErrorEvent struct {
	Error string
}

// UnsubscribeEvent is an event passed to unsubscribe event handler.
type UnsubscribeEvent struct{}

// LeaveEvent has info about user who left channel.
type LeaveEvent struct {
	ClientInfo
}

// JoinEvent has info about user who joined channel.
type JoinEvent struct {
	ClientInfo
}

// PublishEvent has info about received channel Publication.
type PublishEvent struct {
	Publication
}

// PublishHandler is a function to handle messages published in
// channels.
type PublishHandler interface {
	OnPublish(*Subscription, PublishEvent)
}

// JoinHandler is a function to handle join messages.
type JoinHandler interface {
	OnJoin(*Subscription, JoinEvent)
}

// LeaveHandler is a function to handle leave messages.
type LeaveHandler interface {
	OnLeave(*Subscription, LeaveEvent)
}

// UnsubscribeHandler is a function to handle unsubscribe event.
type UnsubscribeHandler interface {
	OnUnsubscribe(*Subscription, UnsubscribeEvent)
}

// SubscribeSuccessHandler is a function to handle subscribe success
// event.
type SubscribeSuccessHandler interface {
	OnSubscribeSuccess(*Subscription, SubscribeSuccessEvent)
}

// SubscribeErrorHandler is a function to handle subscribe error event.
type SubscribeErrorHandler interface {
	OnSubscribeError(*Subscription, SubscribeErrorEvent)
}

// SubscriptionEventHub contains callback functions that will be called when
// corresponding event happens with subscription to channel.
type SubscriptionEventHub struct {
	onPublish          PublishHandler
	onJoin             JoinHandler
	onLeave            LeaveHandler
	onUnsubscribe      UnsubscribeHandler
	onSubscribeSuccess SubscribeSuccessHandler
	onSubscribeError   SubscribeErrorHandler
}

// newSubscriptionEventHub initializes new SubscriptionEventHub.
func newSubscriptionEventHub() *SubscriptionEventHub {
	return &SubscriptionEventHub{}
}

// OnPublish allows to set PublishHandler to SubEventHandler.
func (s *Subscription) OnPublish(handler PublishHandler) {
	s.events.onPublish = handler
}

// OnJoin allows to set JoinHandler to SubEventHandler.
func (s *Subscription) OnJoin(handler JoinHandler) {
	s.events.onJoin = handler
}

// OnLeave allows to set LeaveHandler to SubEventHandler.
func (s *Subscription) OnLeave(handler LeaveHandler) {
	s.events.onLeave = handler
}

// OnUnsubscribe allows to set UnsubscribeHandler to SubEventHandler.
func (s *Subscription) OnUnsubscribe(handler UnsubscribeHandler) {
	s.events.onUnsubscribe = handler
}

// OnSubscribeSuccess allows to set SubscribeSuccessHandler to SubEventHandler.
func (s *Subscription) OnSubscribeSuccess(handler SubscribeSuccessHandler) {
	s.events.onSubscribeSuccess = handler
}

// OnSubscribeError allows to set SubscribeErrorHandler to SubEventHandler.
func (s *Subscription) OnSubscribeError(handler SubscribeErrorHandler) {
	s.events.onSubscribeError = handler
}

// Describe different states of Sub.
const (
	NEW = iota
	SUBSCRIBING
	SUBSCRIBED
	SUBERROR
	UNSUBSCRIBED
)

// Subscription represents client subscription to channel.
type Subscription struct {
	mu              sync.Mutex
	channel         string
	centrifuge      *Client
	status          int
	events          *SubscriptionEventHub
	lastSeq         uint32
	lastGen         uint32
	lastEpoch       string
	resubscribed    bool
	recovered       bool
	recoverable     bool
	recover         bool
	err             error
	needResubscribe bool
	subscribedAt    int64
	subFutures      []chan error
}

func (c *Client) newSubscription(channel string) *Subscription {
	s := &Subscription{
		centrifuge:      c,
		channel:         channel,
		events:          newSubscriptionEventHub(),
		subFutures:      make([]chan error, 0),
		needResubscribe: true,
	}
	return s
}

// Channel returns subscription channel.
func (s *Subscription) Channel() string {
	return s.channel
}

// Status returns current Subscription status.
func (s *Subscription) Status() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Subscription) setSubscribedAt(val int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribedAt = val
}

func (s *Subscription) newSubFuture() chan error {
	fut := make(chan error, 1)
	s.mu.Lock()
	if s.status == SUBSCRIBED {
		fut <- nil
	} else if s.status == SUBERROR {
		fut <- s.err
	} else {
		s.subFutures = append(s.subFutures, fut)
	}
	s.mu.Unlock()
	return fut
}

// Sub.mu lock must be held outside.
func (s *Subscription) resolveSubFutures(err error) {
	for _, ch := range s.subFutures {
		select {
		case ch <- err:
		default:
		}
	}
	s.subFutures = nil
}

func (s *Subscription) removeSubFuture(subFuture chan error) {
	s.mu.Lock()
	for i, v := range s.subFutures {
		if v == subFuture {
			s.subFutures = append(s.subFutures[:i], s.subFutures[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
}

// Publish allows to publish data to channel.
func (s *Subscription) Publish(data []byte) error {
	subFuture := s.newSubFuture()
	select {
	case err := <-subFuture:
		if err != nil {
			return err
		}
		return s.centrifuge.publish(s.channel, data)
	case <-time.After(s.centrifuge.config.ReadTimeout):
		s.removeSubFuture(subFuture)
		return ErrTimeout
	}
}

// History allows to extract channel history.
func (s *Subscription) History() ([]Publication, error) {
	return s.history()
}

// Presence allows to extract channel history.
func (s *Subscription) Presence() (map[string]ClientInfo, error) {
	return s.presence()
}

// PresenceStats allows to extract channel presence stats.
func (s *Subscription) PresenceStats() (PresenceStats, error) {
	return s.presenceStats()
}

func (s *Subscription) history() ([]Publication, error) {
	subFuture := s.newSubFuture()
	select {
	case err := <-subFuture:
		if err != nil {
			return nil, err
		}
		return s.centrifuge.history(s.channel)
	case <-time.After(s.centrifuge.config.ReadTimeout):
		s.removeSubFuture(subFuture)
		return nil, ErrTimeout
	}
}

func (s *Subscription) presence() (map[string]proto.ClientInfo, error) {
	subFuture := s.newSubFuture()
	select {
	case err := <-subFuture:
		if err != nil {
			return nil, err
		}
		return s.centrifuge.presence(s.channel)
	case <-time.After(s.centrifuge.config.ReadTimeout):
		s.removeSubFuture(subFuture)
		return nil, ErrTimeout
	}
}

func (s *Subscription) presenceStats() (PresenceStats, error) {
	subFuture := s.newSubFuture()
	select {
	case err := <-subFuture:
		if err != nil {
			return PresenceStats{}, err
		}
		return s.centrifuge.presenceStats(s.channel)
	case <-time.After(s.centrifuge.config.ReadTimeout):
		s.removeSubFuture(subFuture)
		return PresenceStats{}, ErrTimeout
	}
}

// Unsubscribe allows to unsubscribe from channel.
func (s *Subscription) Unsubscribe() error {
	s.centrifuge.unsubscribe(s.channel)
	s.setSubscribedAt(0)
	s.triggerOnUnsubscribe(false)
	return nil
}

// Subscribe allows to subscribe again after unsubscribing.
func (s *Subscription) Subscribe() error {
	s.mu.Lock()
	s.needResubscribe = true
	s.mu.Unlock()
	return s.resubscribe(false)
}

func (s *Subscription) triggerOnUnsubscribe(needResubscribe bool) {
	s.mu.Lock()
	if s.status != SUBSCRIBED {
		s.status = UNSUBSCRIBED
		s.mu.Unlock()
		return
	}
	s.needResubscribe = needResubscribe
	s.status = UNSUBSCRIBED
	s.mu.Unlock()
	if s.events != nil && s.events.onUnsubscribe != nil {
		handler := s.events.onUnsubscribe
		handler.OnUnsubscribe(s, UnsubscribeEvent{})
	}
}

func (s *Subscription) subscribeSuccess(recovered bool, isResubscribe bool) {
	s.mu.Lock()
	if s.status != SUBSCRIBING {
		s.mu.Unlock()
		return
	}
	s.status = SUBSCRIBED
	s.resolveSubFutures(nil)
	s.mu.Unlock()
	if s.events != nil && s.events.onSubscribeSuccess != nil {
		handler := s.events.onSubscribeSuccess
		ev := SubscribeSuccessEvent{Resubscribed: isResubscribe, Recovered: recovered}
		handler.OnSubscribeSuccess(s, ev)
	}
	s.mu.Lock()
	s.resubscribed = true
	s.mu.Unlock()
}

func (s *Subscription) subscribeError(err error) {
	s.mu.Lock()
	if s.status != SUBSCRIBING {
		s.mu.Unlock()
		return
	}
	s.err = err
	s.status = SUBERROR
	s.resolveSubFutures(err)
	s.mu.Unlock()
	if s.events != nil && s.events.onSubscribeError != nil {
		handler := s.events.onSubscribeError
		handler.OnSubscribeError(s, SubscribeErrorEvent{Error: err.Error()})
	}
}

func (s *Subscription) handlePublication(pub Publication) {
	var handler PublishHandler
	if s.events != nil && s.events.onPublish != nil {
		handler = s.events.onPublish
	}
	s.mu.Lock()
	s.lastSeq = pub.Seq
	s.lastGen = pub.Gen
	s.mu.Unlock()
	if handler != nil {
		handler.OnPublish(s, PublishEvent{Publication: pub})
	}
}

func (s *Subscription) handleJoin(info proto.ClientInfo) {
	var handler JoinHandler
	if s.events != nil && s.events.onJoin != nil {
		handler = s.events.onJoin
	}
	if handler != nil {
		handler.OnJoin(s, JoinEvent{ClientInfo: info})
	}
}

func (s *Subscription) handleLeave(info proto.ClientInfo) {
	var handler LeaveHandler
	if s.events != nil && s.events.onLeave != nil {
		handler = s.events.onLeave
	}
	if handler != nil {
		handler.OnLeave(s, LeaveEvent{ClientInfo: info})
	}
}

func (s *Subscription) handleUnsub(m proto.Unsub) {
	s.Unsubscribe()
	if m.Resubscribe {
		s.Subscribe()
	}
}

func (s *Subscription) resubscribe(isResubscribe bool) error {
	s.mu.Lock()
	if s.status == SUBSCRIBED || s.status == SUBSCRIBING {
		s.mu.Unlock()
		return nil
	}
	needResubscribe := s.needResubscribe
	s.mu.Unlock()

	if !needResubscribe {
		return nil
	}

	s.centrifuge.mutex.Lock()
	if s.centrifuge.status != CONNECTED {
		s.centrifuge.mutex.Unlock()
		return nil
	}
	s.centrifuge.mutex.Unlock()

	s.mu.Lock()
	s.status = SUBSCRIBING
	s.mu.Unlock()

	clientID := s.centrifuge.clientID()
	token, err := s.centrifuge.privateSign(s.channel)
	if s.centrifuge.clientID() != clientID {
		return nil
	}
	if err != nil {
		s.mu.Lock()
		s.status = UNSUBSCRIBED
		s.mu.Unlock()
		return fmt.Errorf("error subscribing on channel %s: %v", s.channel, err)
	}

	s.mu.Lock()
	var recover bool
	var seq uint32
	var gen uint32
	var epoch string
	if s.subscribedAt != 0 && s.recover {
		recover = true
		seq = s.lastSeq
		gen = s.lastGen
		epoch = s.lastEpoch
	}
	s.mu.Unlock()

	res, err := s.centrifuge.sendSubscribe(s.channel, recover, seq, gen, epoch, token)
	if err != nil {
		if err == ErrTimeout {
			s.mu.Lock()
			s.status = NEW
			s.mu.Unlock()
			return err
		}
		s.subscribeError(err)
		return nil
	}

	if res.Expires {
		go func(interval uint32) {
			select {
			case <-s.centrifuge.closeCh:
				return
			case <-time.After(time.Duration(interval) * time.Second):
				s.centrifuge.sendSubRefresh(s.channel)
			}
		}(res.TTL)
	}

	s.mu.Lock()
	previousSubscribedAt := s.subscribedAt
	s.mu.Unlock()

	s.subscribeSuccess(res.Recovered, isResubscribe && previousSubscribedAt != 0)
	s.setSubscribedAt(time.Now().Unix())
	s.processRecover(res)
	return nil
}

func (s *Subscription) processRecover(res proto.SubscribeResult) {
	s.mu.Lock()
	s.lastEpoch = res.Epoch
	s.mu.Unlock()
	if len(res.Publications) > 0 {
		for i := len(res.Publications) - 1; i >= 0; i-- {
			s.handlePublication(*res.Publications[i])
		}
	} else {
		s.mu.Lock()
		s.lastSeq = res.Seq
		s.lastGen = res.Gen
		s.mu.Unlock()
	}
}
