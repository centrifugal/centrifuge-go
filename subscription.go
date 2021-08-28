package centrifuge

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/protocol"
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

// SubscribeSuccessHandler is a function to handle subscribe success event.
type SubscribeSuccessHandler interface {
	OnSubscribeSuccess(*Subscription, SubscribeSuccessEvent)
}

// SubscribeErrorHandler is a function to handle subscribe error event.
type SubscribeErrorHandler interface {
	OnSubscribeError(*Subscription, SubscribeErrorEvent)
}

// subscriptionEventHub contains callback functions that will be called when
// corresponding event happens with subscription to channel.
type subscriptionEventHub struct {
	onPublish          PublishHandler
	onJoin             JoinHandler
	onLeave            LeaveHandler
	onUnsubscribe      UnsubscribeHandler
	onSubscribeSuccess SubscribeSuccessHandler
	onSubscribeError   SubscribeErrorHandler
}

// newSubscriptionEventHub initializes new subscriptionEventHub.
func newSubscriptionEventHub() *subscriptionEventHub {
	return &subscriptionEventHub{}
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

// Different states of Subscription.
const (
	UNSUBSCRIBED = iota
	SUBSCRIBING
	SUBSCRIBED
	SUBERROR
	SUBCLOSED
)

// Subscription represents client subscription to channel.
type Subscription struct {
	futureID        uint64
	mu              sync.Mutex
	channel         string
	centrifuge      *Client
	subCloseCh      chan struct{}
	status          int
	events          *subscriptionEventHub
	lastOffset      uint64
	lastEpoch       string
	recover         bool
	err             error
	needResubscribe bool
	subFutures      map[uint64]subFuture
}

type subFuture struct {
	fn      func(error)
	closeCh chan struct{}
}

func newSubFuture(fn func(error)) subFuture {
	return subFuture{fn: fn, closeCh: make(chan struct{})}
}

func (c *Client) newSubscription(channel string) *Subscription {
	s := &Subscription{
		centrifuge:      c,
		channel:         channel,
		subCloseCh:      make(chan struct{}),
		events:          newSubscriptionEventHub(),
		subFutures:      make(map[uint64]subFuture),
		needResubscribe: false,
	}
	return s
}

// Channel returns subscription channel.
func (s *Subscription) Channel() string {
	return s.channel
}

func (s *Subscription) nextFutureID() uint64 {
	return atomic.AddUint64(&s.futureID, 1)
}

// Sub.mu lock must be held outside.
func (s *Subscription) resolveSubFutures(err error) {
	for _, fut := range s.subFutures {
		fut.fn(err)
		close(fut.closeCh)
	}
	s.subFutures = make(map[uint64]subFuture)
}

// Publish allows to publish data to channel.
func (s *Subscription) Publish(data []byte) (PublishResult, error) {
	s.mu.Lock()
	if s.status == SUBCLOSED {
		s.mu.Unlock()
		return PublishResult{}, ErrSubscriptionClosed
	}
	s.mu.Unlock()

	resCh := make(chan PublishResult, 1)
	errCh := make(chan error, 1)
	s.publish(data, func(result PublishResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

type HistoryOptions struct {
	Limit   int32
	Since   *StreamPosition
	Reverse bool
}

type HistoryOption func(options *HistoryOptions)

func WithHistorySince(sp *StreamPosition) HistoryOption {
	return func(options *HistoryOptions) {
		options.Since = sp
	}
}

func WithHistoryLimit(limit int32) HistoryOption {
	return func(options *HistoryOptions) {
		options.Limit = limit
	}
}

func WithHistoryReverse(reverse bool) HistoryOption {
	return func(options *HistoryOptions) {
		options.Reverse = reverse
	}
}

// History allows to extract channel history. By default it returns current stream top
// position without publications. Use WithHistoryLimit with a value > 0 to make this func
// to return publications.
func (s *Subscription) History(opts ...HistoryOption) (HistoryResult, error) {
	historyOpts := &HistoryOptions{}
	for _, opt := range opts {
		opt(historyOpts)
	}
	s.mu.Lock()
	if s.status == SUBCLOSED {
		s.mu.Unlock()
		return HistoryResult{}, ErrSubscriptionClosed
	}
	s.mu.Unlock()

	resCh := make(chan HistoryResult, 1)
	errCh := make(chan error, 1)
	s.history(*historyOpts, func(result HistoryResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

// Presence allows to extract channel history.
func (s *Subscription) Presence() (PresenceResult, error) {
	s.mu.Lock()
	if s.status == SUBCLOSED {
		s.mu.Unlock()
		return PresenceResult{}, ErrSubscriptionClosed
	}
	s.mu.Unlock()

	resCh := make(chan PresenceResult, 1)
	errCh := make(chan error, 1)
	s.presence(func(result PresenceResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

// PresenceStats allows to extract channel presence stats.
func (s *Subscription) PresenceStats() (PresenceStatsResult, error) {
	resCh := make(chan PresenceStatsResult, 1)
	errCh := make(chan error, 1)
	s.presenceStats(func(result PresenceStatsResult, err error) {
		resCh <- result
		errCh <- err
	})
	return <-resCh, <-errCh
}

func (s *Subscription) onSubscribe(fn func(err error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == SUBSCRIBED {
		go fn(nil)
	} else if s.status == SUBERROR {
		go fn(s.err)
	} else {
		id := s.nextFutureID()
		fut := newSubFuture(fn)
		s.subFutures[id] = fut
		go func() {
			select {
			case <-fut.closeCh:
			case <-time.After(s.centrifuge.config.ReadTimeout):
				s.mu.Lock()
				defer s.mu.Unlock()
				fut, ok := s.subFutures[id]
				if !ok {
					return
				}
				delete(s.subFutures, id)
				fut.fn(ErrTimeout)
			}
		}()
	}
}

func (s *Subscription) publish(data []byte, fn func(PublishResult, error)) {
	s.onSubscribe(func(err error) {
		if err != nil {
			fn(PublishResult{}, err)
			return
		}
		s.centrifuge.publish(s.channel, data, fn)
	})
}

func (s *Subscription) history(opts HistoryOptions, fn func(HistoryResult, error)) {
	s.onSubscribe(func(err error) {
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		s.centrifuge.history(s.channel, opts, fn)
	})
}

func (s *Subscription) presence(fn func(PresenceResult, error)) {
	s.onSubscribe(func(err error) {
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		s.centrifuge.presence(s.channel, fn)
	})
}

func (s *Subscription) presenceStats(fn func(PresenceStatsResult, error)) {
	s.onSubscribe(func(err error) {
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		s.centrifuge.presenceStats(s.channel, fn)
	})
}

// Unsubscribe allows to unsubscribe from channel.
func (s *Subscription) Unsubscribe() error {
	s.mu.Lock()
	if s.status == SUBCLOSED {
		s.mu.Unlock()
		return ErrSubscriptionClosed
	}
	s.mu.Unlock()
	s.triggerOnUnsubscribe(false, false)
	s.centrifuge.unsubscribe(s.channel, func(result UnsubscribeResult, err error) {})
	return nil
}

// Close unsubscribes from channel and removes Subscription from Client's
// subscription map.
func (s *Subscription) Close() error {
	var needUnsubscribeEvent bool
	s.mu.Lock()
	if s.status == SUBCLOSED {
		s.mu.Unlock()
		return ErrSubscriptionClosed
	}
	if s.status == SUBSCRIBED {
		needUnsubscribeEvent = true
	}
	s.status = SUBCLOSED
	s.mu.Unlock()
	s.centrifuge.removeSubscription(s.channel)
	s.centrifuge.unsubscribe(s.channel, func(result UnsubscribeResult, err error) {})
	if needUnsubscribeEvent {
		if s.events != nil && s.events.onUnsubscribe != nil {
			handler := s.events.onUnsubscribe
			s.centrifuge.runHandler(func() {
				handler.OnUnsubscribe(s, UnsubscribeEvent{})
			})
		}
	}
	return nil
}

type SubscribeOptions struct {
	Since *StreamPosition
}

type SubscribeOption func(options *SubscribeOptions)

func WithSubscribeSince(sp *StreamPosition) SubscribeOption {
	return func(options *SubscribeOptions) {
		options.Since = sp
	}
}

// Subscribe allows initiating subscription process.
func (s *Subscription) Subscribe(opts ...SubscribeOption) error {
	subscribeOpts := &SubscribeOptions{}
	for _, opt := range opts {
		opt(subscribeOpts)
	}
	s.mu.Lock()
	if s.status == SUBCLOSED {
		s.mu.Unlock()
		return ErrSubscriptionClosed
	}
	s.needResubscribe = true
	s.mu.Unlock()
	if !s.centrifuge.connected() {
		return nil
	}
	return s.resubscribe(false, s.centrifuge.clientID(), *subscribeOpts)
}

func (s *Subscription) triggerOnUnsubscribe(needResubscribe bool, needRecover bool) {
	s.mu.Lock()
	if s.status != SUBSCRIBED {
		s.status = UNSUBSCRIBED
		s.mu.Unlock()
		return
	}
	s.needResubscribe = needResubscribe
	s.recover = needRecover
	s.status = UNSUBSCRIBED
	s.mu.Unlock()
	if s.events != nil && s.events.onUnsubscribe != nil {
		handler := s.events.onUnsubscribe
		s.centrifuge.runHandler(func() {
			handler.OnUnsubscribe(s, UnsubscribeEvent{})
		})
	}
}

func (s *Subscription) subscribeSuccess(isResubscribe bool, res *protocol.SubscribeResult) {
	s.mu.Lock()
	if s.status != SUBSCRIBING {
		s.mu.Unlock()
		return
	}
	closeCh := make(chan struct{})
	s.subCloseCh = closeCh
	s.runSubRefresh(res.Ttl, closeCh)
	s.status = SUBSCRIBED
	s.resolveSubFutures(nil)
	s.mu.Unlock()
	if s.events != nil && s.events.onSubscribeSuccess != nil {
		handler := s.events.onSubscribeSuccess
		ev := SubscribeSuccessEvent{Resubscribed: isResubscribe, Recovered: res.Recovered}
		s.centrifuge.runHandler(func() {
			handler.OnSubscribeSuccess(s, ev)
		})
	}
	s.processRecover(res)
}

func (s *Subscription) subscribeError(err error) {
	s.mu.Lock()
	if s.status != SUBSCRIBING {
		s.mu.Unlock()
		return
	}
	if err == ErrTimeout {
		s.status = UNSUBSCRIBED
		s.mu.Unlock()
		go s.centrifuge.handleDisconnect(&disconnect{"subscribe timeout", true})
		return
	}
	s.err = err
	s.status = SUBERROR
	s.resolveSubFutures(err)
	s.mu.Unlock()
	if s.events != nil && s.events.onSubscribeError != nil {
		handler := s.events.onSubscribeError
		s.centrifuge.runHandler(func() {
			handler.OnSubscribeError(s, SubscribeErrorEvent{Error: err.Error()})
		})
	}
}

func (s *Subscription) handlePublication(pub *protocol.Publication) {
	var handler PublishHandler
	if s.events != nil && s.events.onPublish != nil {
		handler = s.events.onPublish
	}
	if handler != nil {
		s.centrifuge.runHandler(func() {
			handler.OnPublish(s, PublishEvent{Publication: pubFromProto(pub)})
			s.mu.Lock()
			if pub.Offset > 0 {
				s.lastOffset = pub.Offset
			}
			s.mu.Unlock()
		})
	}
}

func (s *Subscription) handleJoin(info *protocol.ClientInfo) {
	var handler JoinHandler
	if s.events != nil && s.events.onJoin != nil {
		handler = s.events.onJoin
	}
	if handler != nil {
		s.centrifuge.runHandler(func() {
			handler.OnJoin(s, JoinEvent{ClientInfo: infoFromProto(info)})
		})
	}
}

func (s *Subscription) handleLeave(info *protocol.ClientInfo) {
	var handler LeaveHandler
	if s.events != nil && s.events.onLeave != nil {
		handler = s.events.onLeave
	}
	if handler != nil {
		s.centrifuge.runHandler(func() {
			handler.OnLeave(s, LeaveEvent{ClientInfo: infoFromProto(info)})
		})
	}
}

func (s *Subscription) handleUnsubscribe(_ *protocol.Unsubscribe) {
	_ = s.Unsubscribe()
}

func (s *Subscription) resubscribe(isResubscribe bool, clientID string, opts SubscribeOptions) error {
	s.mu.Lock()
	if s.status == SUBSCRIBED || s.status == SUBSCRIBING {
		s.mu.Unlock()
		return nil
	}
	needResubscribe := s.needResubscribe
	if !needResubscribe {
		s.mu.Unlock()
		return nil
	}

	s.status = SUBSCRIBING
	s.mu.Unlock()

	token, err := s.centrifuge.privateSign(s.channel, clientID)
	if err != nil {
		s.mu.Lock()
		s.status = UNSUBSCRIBED
		s.mu.Unlock()
		return fmt.Errorf("error subscribing on channel %s: %v", s.channel, err)
	}

	s.mu.Lock()
	if s.status != SUBSCRIBING {
		s.mu.Unlock()
		return nil
	}

	var isRecover bool
	var sp StreamPosition
	if s.recover && isResubscribe {
		isRecover = true
		sp.Offset = s.lastOffset
		sp.Epoch = s.lastEpoch
	}
	if opts.Since != nil {
		s.recover = true
		isRecover = true
		sp.Offset = opts.Since.Offset
		sp.Epoch = opts.Since.Epoch
	}

	err = s.centrifuge.sendSubscribe(s.channel, isRecover, sp, token, func(res *protocol.SubscribeResult, err error) {
		if err != nil {
			s.subscribeError(err)
			return
		}
		s.subscribeSuccess(isRecover, res)
	})
	s.mu.Unlock()
	return err
}

func (s *Subscription) runSubRefresh(ttl uint32, closeCh chan struct{}) {
	if s.status != SUBSCRIBED {
		return
	}
	if ttl == 0 {
		return
	}
	go func(interval uint32) {
		select {
		case <-closeCh:
			return
		case <-time.After(time.Duration(interval) * time.Second):
			s.centrifuge.sendSubRefresh(s.channel, func(result *protocol.SubRefreshResult, err error) {
				if err != nil {
					return
				}
				if !result.Expires {
					return
				}
				s.mu.Lock()
				s.runSubRefresh(result.Ttl, closeCh)
				s.mu.Unlock()
			})
		}
	}(ttl)
}

func (s *Subscription) processRecover(res *protocol.SubscribeResult) {
	s.mu.Lock()
	s.lastEpoch = res.Epoch
	s.mu.Unlock()
	if len(res.Publications) > 0 {
		pubs := res.Publications
		for i := 0; i < len(pubs); i++ {
			s.handlePublication(res.Publications[i])
		}
	} else {
		s.mu.Lock()
		s.lastOffset = res.Offset
		s.mu.Unlock()
	}
}
