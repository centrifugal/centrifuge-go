package centrifuge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/protocol"
	fossil "github.com/shadowspore/fossil-delta"
)

// SubState represents state of Subscription.
type SubState string

// Different states of Subscription.
const (
	SubStateUnsubscribed SubState = "unsubscribed"
	SubStateSubscribing  SubState = "subscribing"
	SubStateSubscribed   SubState = "subscribed"
)

// DeltaType represents type of delta used for Subscription.
type DeltaType string

const (
	// DeltaTypeNone means that no delta is used for this subscription.
	DeltaTypeNone DeltaType = ""
	// DeltaTypeFossil means Fossil-based delta.
	DeltaTypeFossil DeltaType = "fossil"
)

// SubscriptionConfig allows setting Subscription options.
type SubscriptionConfig struct {
	// Data is an arbitrary data to pass to a server in each subscribe request.
	Data []byte
	// Token for Subscription.
	Token string
	// GetToken called to get or refresh private channel subscription token.
	GetToken func(SubscriptionTokenEvent) (string, error)
	// Positioned flag asks server to make Subscription positioned. Only makes sense
	// in channels with history stream on.
	Positioned bool
	// Recoverable flag asks server to make Subscription recoverable. Only makes sense
	// in channels with history stream on.
	Recoverable bool
	// JoinLeave flag asks server to push join/leave messages.
	JoinLeave bool
	// Delta allows to specify delta type for the subscription. By default, no delta is used.
	Delta DeltaType
	// MinResubscribeDelay is the minimum delay between resubscription attempts.
	// This delay is jittered.
	// Zero value means 200 * time.Millisecond.
	MinResubscribeDelay time.Duration
	// MaxResubscribeDelay is the maximum delay between resubscription attempts.
	// Zero value means 20 * time.Second.
	MaxResubscribeDelay time.Duration
	// GetState called to load the app's current state and stream position.
	// Requires Centrifugo >= 6.8.0.
	//
	// The SDK calls this:
	// - On initial subscribe (no saved position)
	// - On reconnect when recovery fails (server returns error 112 —
	//   unrecoverable position)
	//
	// NOT called on reconnects where the server successfully recovers missed
	// publications — in that case the recovered publications arrive as events
	// and GetState is skipped.
	//
	// The app should load its data from its own source of truth (database,
	// API), render it, and return the stream position. The SDK subscribes with
	// recovery from the returned position, so any publications between the
	// state read and the subscribe are delivered as publication events.
	//
	// IMPORTANT: inside GetState, read the stream position FIRST, then read
	// your data. This ensures the position is a lower bound — any data loaded
	// after the position read is guaranteed to be included. The reverse order
	// can produce gaps.
	//
	// Recovered publications may overlap with data already loaded in GetState.
	// This works correctly when updates are idempotent (applying the same
	// update twice produces the same result). For non-idempotent updates,
	// deduplicate by publication offset.
	//
	// On error, the SDK emits an error event with SubscriptionGetStateError
	// and retries with backoff.
	GetState func(SubscriptionGetStateEvent) (StreamPosition, error)
}

func newSubscription(c *Client, channel string, config ...SubscriptionConfig) *Subscription {
	var resubscribeStrategy reconnectStrategy
	var minResubscribeDelay, maxResubscribeDelay time.Duration
	if len(config) == 1 {
		minResubscribeDelay = config[0].MinResubscribeDelay
		maxResubscribeDelay = config[0].MaxResubscribeDelay
	}
	resubscribeStrategy = newBackoffReconnect(minResubscribeDelay, maxResubscribeDelay)
	s := &Subscription{
		Channel:             channel,
		centrifuge:          c,
		state:               SubStateUnsubscribed,
		events:              newSubscriptionEventHub(),
		subFutures:          make(map[uint64]subFuture),
		resubscribeStrategy: resubscribeStrategy,
	}
	if len(config) == 1 {
		cfg := config[0]
		s.token = cfg.Token
		s.getToken = cfg.GetToken
		s.data = cfg.Data
		s.positioned = cfg.Positioned
		s.recoverable = cfg.Recoverable
		s.joinLeave = cfg.JoinLeave
		s.deltaType = cfg.Delta
		s.getState = cfg.GetState
	}
	return s
}

// Subscription represents client subscription to channel. DO NOT initialize this struct
// directly, instead use Client.NewSubscription method to create channel subscriptions.
type Subscription struct {
	futureID uint64 // Keep atomic on top!

	mu         sync.RWMutex
	centrifuge *Client

	// Channel for a subscription.
	Channel string

	state SubState

	events     *subscriptionEventHub
	offset     uint64
	epoch      string
	recover    bool
	subFutures map[uint64]subFuture
	data       []byte

	positioned  bool
	recoverable bool
	joinLeave   bool

	token    string
	getToken func(SubscriptionTokenEvent) (string, error)
	getState func(SubscriptionGetStateEvent) (StreamPosition, error)

	resubscribeAttempts int
	resubscribeStrategy reconnectStrategy

	resubscribeTimer *time.Timer
	refreshTimer     *time.Timer

	deltaType       DeltaType
	deltaNegotiated bool
	prevData        []byte

	// pushID is the numeric channel ID assigned by the server when channel
	// compaction is negotiated. Pushes then carry this ID instead of the
	// channel name. Guarded by mu.
	pushID int64

	inflight atomic.Bool
}

func (s *Subscription) State() SubState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

type subFuture struct {
	fn      func(error)
	closeCh chan struct{}
}

func newSubFuture(fn func(error)) subFuture {
	return subFuture{fn: fn, closeCh: make(chan struct{})}
}

func (s *Subscription) nextFutureID() uint64 {
	return atomic.AddUint64(&s.futureID, 1)
}

// Lock must be held outside.
func (s *Subscription) resolveSubFutures(err error) {
	for _, fut := range s.subFutures {
		fut.fn(err)
		close(fut.closeCh)
	}
	s.subFutures = make(map[uint64]subFuture)
}

// Publish allows publishing data to the subscription channel.
func (s *Subscription) Publish(ctx context.Context, data []byte) (PublishResult, error) {
	s.mu.Lock()
	if s.state == SubStateUnsubscribed {
		s.mu.Unlock()
		return PublishResult{}, ErrSubscriptionUnsubscribed
	}
	s.mu.Unlock()

	resCh := make(chan PublishResult, 1)
	errCh := make(chan error, 1)
	s.publish(ctx, data, func(result PublishResult, err error) {
		resCh <- result
		errCh <- err
	})
	select {
	case <-ctx.Done():
		return PublishResult{}, ctx.Err()
	case res := <-resCh:
		return res, <-errCh
	}
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

// History allows extracting channel history. By default, it returns current stream top
// position without publications. Use WithHistoryLimit with a value > 0 to make this func
// to return publications.
func (s *Subscription) History(ctx context.Context, opts ...HistoryOption) (HistoryResult, error) {
	historyOpts := &HistoryOptions{}
	for _, opt := range opts {
		opt(historyOpts)
	}
	s.mu.Lock()
	if s.state == SubStateUnsubscribed {
		s.mu.Unlock()
		return HistoryResult{}, ErrSubscriptionUnsubscribed
	}
	s.mu.Unlock()

	resCh := make(chan HistoryResult, 1)
	errCh := make(chan error, 1)
	s.history(ctx, *historyOpts, func(result HistoryResult, err error) {
		resCh <- result
		errCh <- err
	})
	select {
	case <-ctx.Done():
		return HistoryResult{}, ctx.Err()
	case res := <-resCh:
		return res, <-errCh
	}
}

// Presence allows extracting channel presence.
func (s *Subscription) Presence(ctx context.Context) (PresenceResult, error) {
	s.mu.Lock()
	if s.state == SubStateUnsubscribed {
		s.mu.Unlock()
		return PresenceResult{}, ErrSubscriptionUnsubscribed
	}
	s.mu.Unlock()

	resCh := make(chan PresenceResult, 1)
	errCh := make(chan error, 1)
	s.presence(ctx, func(result PresenceResult, err error) {
		resCh <- result
		errCh <- err
	})
	select {
	case <-ctx.Done():
		return PresenceResult{}, ctx.Err()
	case res := <-resCh:
		return res, <-errCh
	}
}

// PresenceStats allows extracting channel presence stats.
func (s *Subscription) PresenceStats(ctx context.Context) (PresenceStatsResult, error) {
	s.mu.Lock()
	if s.state == SubStateUnsubscribed {
		s.mu.Unlock()
		return PresenceStatsResult{}, ErrSubscriptionUnsubscribed
	}
	s.mu.Unlock()

	resCh := make(chan PresenceStatsResult, 1)
	errCh := make(chan error, 1)
	s.presenceStats(ctx, func(result PresenceStatsResult, err error) {
		resCh <- result
		errCh <- err
	})
	select {
	case <-ctx.Done():
		return PresenceStatsResult{}, ctx.Err()
	case res := <-resCh:
		return res, <-errCh
	}
}

func (s *Subscription) onSubscribe(fn func(err error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case SubStateSubscribed:
		go fn(nil)
	case SubStateUnsubscribed:
		go fn(ErrSubscriptionUnsubscribed)
	default:
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

func (s *Subscription) publish(ctx context.Context, data []byte, fn func(PublishResult, error)) {
	s.onSubscribe(func(err error) {
		select {
		case <-ctx.Done():
			fn(PublishResult{}, ctx.Err())
			return
		default:
		}
		if err != nil {
			fn(PublishResult{}, err)
			return
		}
		s.centrifuge.publish(ctx, s.Channel, data, fn)
	})
}

func (s *Subscription) history(ctx context.Context, opts HistoryOptions, fn func(HistoryResult, error)) {
	s.onSubscribe(func(err error) {
		select {
		case <-ctx.Done():
			fn(HistoryResult{}, ctx.Err())
			return
		default:
		}
		if err != nil {
			fn(HistoryResult{}, err)
			return
		}
		s.centrifuge.history(ctx, s.Channel, opts, fn)
	})
}

func (s *Subscription) presence(ctx context.Context, fn func(PresenceResult, error)) {
	s.onSubscribe(func(err error) {
		select {
		case <-ctx.Done():
			fn(PresenceResult{}, ctx.Err())
			return
		default:
		}
		if err != nil {
			fn(PresenceResult{}, err)
			return
		}
		s.centrifuge.presence(ctx, s.Channel, fn)
	})
}

func (s *Subscription) presenceStats(ctx context.Context, fn func(PresenceStatsResult, error)) {
	s.onSubscribe(func(err error) {
		select {
		case <-ctx.Done():
			fn(PresenceStatsResult{}, ctx.Err())
			return
		default:
		}
		if err != nil {
			fn(PresenceStatsResult{}, err)
			return
		}
		s.centrifuge.presenceStats(ctx, s.Channel, fn)
	})
}

// Unsubscribe allows unsubscribing from channel.
func (s *Subscription) Unsubscribe() error {
	if s.centrifuge.isClosed() {
		return ErrClientClosed
	}
	s.unsubscribe(unsubscribedUnsubscribeCalled, "unsubscribe called", true)
	return nil
}

func (s *Subscription) unsubscribe(code uint32, reason string, sendUnsubscribe bool) {
	s.moveToUnsubscribed(code, reason)
	if sendUnsubscribe {
		s.centrifuge.unsubscribe(s.Channel, func(result UnsubscribeResult, err error) {
			if err != nil {
				go s.centrifuge.handleDisconnect(&disconnect{Code: connectingUnsubscribeError, Reason: "unsubscribe error", Reconnect: true})
				return
			}
		})
	}
}

// Subscribe allows initiating subscription process.
func (s *Subscription) Subscribe() error {
	if s.centrifuge.isClosed() {
		return ErrClientClosed
	}
	s.mu.Lock()
	if s.state == SubStateSubscribed || s.state == SubStateSubscribing {
		s.mu.Unlock()
		return nil
	}
	s.state = SubStateSubscribing
	s.mu.Unlock()

	if s.events != nil && s.events.onSubscribing != nil {
		handler := s.events.onSubscribing
		s.centrifuge.runHandlerAsync(func() {
			handler(SubscribingEvent{
				Code:   subscribingSubscribeCalled,
				Reason: "subscribe called",
			})
		})
	}

	if !s.centrifuge.isConnected() {
		return nil
	}
	s.resubscribe()
	return nil
}

func (s *Subscription) moveToUnsubscribed(code uint32, reason string) {
	s.mu.Lock()
	s.resubscribeAttempts = 0
	if s.resubscribeTimer != nil {
		s.resubscribeTimer.Stop()
	}
	if s.refreshTimer != nil {
		s.refreshTimer.Stop()
	}

	needEvent := s.state != SubStateUnsubscribed
	s.state = SubStateUnsubscribed
	// Channel compaction ID is no longer valid once unsubscribed. Cleared under
	// s.mu, atomically with the state transition (see moveToSubscribed).
	if s.pushID != 0 {
		s.centrifuge.updateSubscriptionPushID(s, s.pushID, 0)
		s.pushID = 0
	}
	s.mu.Unlock()

	if needEvent && s.events != nil && s.events.onUnsubscribe != nil {
		handler := s.events.onUnsubscribe
		s.centrifuge.runHandlerAsync(func() {
			handler(UnsubscribedEvent{
				Code:   code,
				Reason: reason,
			})
		})
	}
}

func (s *Subscription) moveToSubscribing(code uint32, reason string) {
	s.mu.Lock()
	s.resubscribeAttempts = 0
	if s.resubscribeTimer != nil {
		s.resubscribeTimer.Stop()
	}
	if s.refreshTimer != nil {
		s.refreshTimer.Stop()
	}
	needEvent := s.state != SubStateSubscribing
	s.state = SubStateSubscribing
	s.mu.Unlock()

	if needEvent && s.events != nil && s.events.onSubscribing != nil {
		handler := s.events.onSubscribing
		s.centrifuge.runHandlerAsync(func() {
			handler(SubscribingEvent{
				Code:   code,
				Reason: reason,
			})
		})
	}
}

func (s *Subscription) moveToSubscribed(res *protocol.SubscribeResult, connGeneration int64) {
	s.mu.Lock()
	if s.state != SubStateSubscribing {
		s.mu.Unlock()
		return
	}
	// Stale-reply guard: the client left the connected session this reply
	// belongs to (transport closed / no ping / disconnect) after the reply
	// arrived but before it was processed. Applying it would flip the
	// subscription to Subscribed while the client is reconnecting, and the
	// post-reconnect resubscribe sweep would then skip it — stranding the
	// subscription without a server-side counterpart. The generation is bumped
	// in clearConnectedState before subscriptions are moved to subscribing, so
	// observing a matching generation here guarantees the teardown has not
	// started touching subscription state yet. Schedule a retry ourselves:
	// the resubscribe-on-connect sweep may have already run and skipped this
	// subscription while the reply was still inflight.
	if connGeneration != s.centrifuge.connGeneration.Load() {
		s.scheduleResubscribe()
		s.mu.Unlock()
		return
	}
	s.state = SubStateSubscribed
	if res.Expires {
		s.scheduleSubRefresh(res.Ttl)
	}
	if res.Recoverable {
		s.recover = true
	}
	s.resubscribeAttempts = 0
	if s.resubscribeTimer != nil {
		s.resubscribeTimer.Stop()
	}
	s.resolveSubFutures(nil)
	s.offset = res.Offset
	s.epoch = res.Epoch
	s.deltaNegotiated = res.Delta
	// Channel compaction: register the numeric channel ID assigned by the
	// server (0 when not negotiated — also clears a stale ID from a previous
	// subscribe session). Always re-registers even when the ID is unchanged:
	// the registry is dropped on teardown and on reconnect the server commonly
	// assigns the same ID again. Done under s.mu, atomically with the state
	// transition, so a concurrent unsubscribe either prevents the registration
	// or runs its own clear strictly after it.
	oldPushID := s.pushID
	s.pushID = res.Id
	s.centrifuge.updateSubscriptionPushID(s, oldPushID, res.Id)
	s.mu.Unlock()

	if s.events != nil && s.events.onSubscribed != nil {
		handler := s.events.onSubscribed
		ev := SubscribedEvent{
			Data:          res.GetData(),
			Recovered:     res.GetRecovered(),
			WasRecovering: res.GetWasRecovering(),
			Recoverable:   res.GetRecoverable(),
			Positioned:    res.GetPositioned(),
		}
		if ev.Positioned || ev.Recoverable {
			ev.StreamPosition = &StreamPosition{
				Epoch:  res.GetEpoch(),
				Offset: res.GetOffset(),
			}
		}
		s.centrifuge.runHandlerSync(func() {
			handler(ev)
		})
	}

	if len(res.Publications) > 0 {
		s.centrifuge.runHandlerSync(func() {
			pubs := res.Publications
			for i := 0; i < len(pubs); i++ {
				pub := res.Publications[i]
				s.mu.Lock()
				if s.state != SubStateSubscribed {
					s.mu.Unlock()
					return
				}
				if pub.Offset > 0 {
					s.offset = pub.Offset
				}
				publicationEvent := PublicationEvent{Publication: pubFromProto(pub)}
				publicationEvent = s.applyDeltaLocked(pub, publicationEvent)
				s.mu.Unlock()
				var handler PublicationHandler
				if s.events != nil && s.events.onPublication != nil {
					handler = s.events.onPublication
				}
				if handler != nil {
					handler(publicationEvent)
				}
			}
		})
	}
}

func (s *Subscription) applyDeltaLocked(pub *protocol.Publication, event PublicationEvent) PublicationEvent {
	if !s.deltaNegotiated {
		return event
	}
	if s.centrifuge.protocolType == protocol.TypeJSON {
		if pub.Delta {
			// pub.Data is JSON string delta, let's decode to []byte and apply it to prevData.
			var delta string
			err := json.Unmarshal(pub.Data, &delta)
			if err != nil {
				panic(err)
			}
			newData, err := fossil.Apply(s.prevData, []byte(delta))
			if err != nil {
				panic(err)
			}
			event.Data = newData
			s.prevData = newData
		} else {
			// pub.Data is JSON string, let's decode to []byte and keep as prevData.
			var data string
			err := json.Unmarshal(pub.Data, &data)
			if err != nil {
				panic(err)
			}
			s.prevData = []byte(data)
			event.Data = s.prevData
		}
	} else {
		if pub.Delta {
			newData, err := fossil.Apply(s.prevData, pub.Data)
			if err != nil {
				panic(err)
			}
			event.Data = newData
			s.prevData = newData
		} else {
			s.prevData = pub.Data
		}
	}
	return event
}

// Lock must be held outside.
func (s *Subscription) scheduleResubscribe() {
	if s.resubscribeTimer != nil {
		if s.centrifuge.logLevelEnabled(LogLevelDebug) {
			s.centrifuge.log(LogLevelDebug, "stopping previous resubscribe timer", map[string]string{
				"channel": s.Channel,
			})
		}
		s.resubscribeTimer.Stop()
		s.resubscribeTimer = nil
	}
	delay := s.resubscribeStrategy.timeBeforeNextAttempt(s.resubscribeAttempts)
	s.resubscribeAttempts++
	s.resubscribeTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if s.state != SubStateSubscribing {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.resubscribe()
	})
}

func (s *Subscription) subscribeError(err error) {
	s.mu.Lock()
	if s.state != SubStateSubscribing {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if errors.Is(err, ErrTimeout) {
		go s.centrifuge.handleDisconnect(&disconnect{Code: connectingSubscribeTimeout, Reason: "subscribe timeout", Reconnect: true})
		return
	}

	var serverError *Error
	if errors.As(err, &serverError) && serverError.Code == errorCodeUnrecoverablePosition && s.getState != nil {
		// Unrecoverable position with GetState: reset position so the next
		// subscribe attempt calls GetState to reload app state from scratch.
		s.mu.Lock()
		s.recover = false
		s.offset = 0
		s.epoch = ""
		s.prevData = nil
		s.scheduleResubscribe()
		s.mu.Unlock()
		return
	}

	s.emitError(SubscriptionSubscribeError{Err: err})

	if errors.As(err, &serverError) {
		if serverError.Code == 109 { // Token expired.
			s.mu.Lock()
			s.token = ""
			s.scheduleResubscribe()
			s.mu.Unlock()
		} else if serverError.Temporary {
			s.mu.Lock()
			s.scheduleResubscribe()
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.resolveSubFutures(err)
			s.mu.Unlock()
			s.unsubscribe(serverError.Code, serverError.Message, false)
		}
	} else {
		s.mu.Lock()
		s.scheduleResubscribe()
		s.mu.Unlock()
	}
}

// Lock must be held outside.
func (s *Subscription) emitError(err error) {
	if s.events != nil && s.events.onError != nil {
		handler := s.events.onError
		s.centrifuge.runHandlerSync(func() {
			handler(SubscriptionErrorEvent{Error: err})
		})
	}
}

func (s *Subscription) handlePublication(pub *protocol.Publication) {
	s.mu.Lock()
	if s.state != SubStateSubscribed {
		s.mu.Unlock()
		return
	}
	if pub.Offset > 0 {
		s.offset = pub.Offset
	}
	publicationEvent := PublicationEvent{Publication: pubFromProto(pub)}
	publicationEvent = s.applyDeltaLocked(pub, publicationEvent)
	s.mu.Unlock()

	var handler PublicationHandler
	if s.events != nil && s.events.onPublication != nil {
		handler = s.events.onPublication
	}
	if handler == nil {
		return
	}
	s.centrifuge.runHandlerSync(func() {
		handler(publicationEvent)
	})
}

func (s *Subscription) handleJoin(info *protocol.ClientInfo) {
	var handler JoinHandler
	if s.events != nil && s.events.onJoin != nil {
		handler = s.events.onJoin
	}
	if handler != nil {
		s.centrifuge.runHandlerSync(func() {
			handler(JoinEvent{ClientInfo: infoFromProto(info)})
		})
	}
}

func (s *Subscription) handleLeave(info *protocol.ClientInfo) {
	var handler LeaveHandler
	if s.events != nil && s.events.onLeave != nil {
		handler = s.events.onLeave
	}
	if handler != nil {
		s.centrifuge.runHandlerSync(func() {
			handler(LeaveEvent{ClientInfo: infoFromProto(info)})
		})
	}
}

func (s *Subscription) handleUnsubscribe(unsubscribe *protocol.Unsubscribe) {
	if unsubscribe.Code < 2500 {
		s.moveToUnsubscribed(unsubscribe.Code, unsubscribe.Reason)
	} else {
		s.moveToSubscribing(unsubscribe.Code, unsubscribe.Reason)
		s.resubscribe()
	}
}

func (s *Subscription) resubscribe() {
	s.mu.Lock()
	if s.state != SubStateSubscribing {
		s.mu.Unlock()
		return
	}
	if s.inflight.Load() {
		s.mu.Unlock()
		if s.centrifuge.logLevelEnabled(LogLevelDebug) {
			s.centrifuge.log(LogLevelDebug, "avoid subscribe since inflight", map[string]string{
				"channel": s.Channel,
			})
		}
		return
	}
	// GetState: ask the app for its current state position. Only called when
	// we don't have a saved position (first subscribe or after a position
	// reset due to unrecoverable position error 112). On normal reconnects
	// with a valid saved position we skip GetState and let the server try
	// recovery — GetState is only called again if recovery fails.
	needGetState := s.getState != nil && !s.recover
	s.inflight.Store(true)
	s.mu.Unlock()

	if needGetState {
		// Run GetState on its own goroutine: resubscribe may be entered with
		// the client mutex held (on connect), and the user callback can be
		// slow (database read) or call client methods. inflight stays true
		// while GetState is running — concurrent resubscribe attempts skip.
		go s.getStateAndResubscribe()
		return
	}
	s.continueResubscribe()
}

func (s *Subscription) getStateAndResubscribe() {
	sp, err := s.getState(SubscriptionGetStateEvent{Channel: s.Channel})
	if err != nil {
		s.inflight.Store(false)
		s.mu.Lock()
		if s.state != SubStateSubscribing {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.emitError(SubscriptionGetStateError{Err: err})
		s.mu.Lock()
		if s.state == SubStateSubscribing {
			s.scheduleResubscribe()
		}
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	if s.state != SubStateSubscribing {
		s.inflight.Store(false)
		s.mu.Unlock()
		return
	}
	s.recover = true
	s.offset = sp.Offset
	s.epoch = sp.Epoch
	s.mu.Unlock()
	s.continueResubscribe()
}

// continueResubscribe is the part of the subscribe flow after the optional
// GetState step: token retrieval and sending the subscribe command. Called
// with inflight already set to true.
func (s *Subscription) continueResubscribe() {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()

	if token == "" && s.getToken != nil {
		var err error
		token, err = s.getSubscriptionToken(s.Channel)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				s.inflight.Store(false)
				s.unsubscribe(unsubscribedUnauthorized, "unauthorized", false)
				return
			}
			s.inflight.Store(false)
			s.subscribeError(err)
			return
		}
		s.mu.Lock()
		if token == "" {
			s.mu.Unlock()
			s.inflight.Store(false)
			s.unsubscribe(unsubscribedUnauthorized, "unauthorized", false)
			return
		}
		s.token = token
		s.mu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != SubStateSubscribing {
		s.inflight.Store(false)
		return
	}

	var isRecover bool
	var sp StreamPosition
	if s.recover {
		isRecover = true
		sp.Offset = s.offset
		sp.Epoch = s.epoch
	}

	// Always offer channel compaction: when the server supports and allows it,
	// the subscribe result carries a numeric channel ID and subsequent pushes
	// use that ID instead of the string channel name.
	flag := subscriptionFlagChannelCompaction
	if s.getState != nil {
		// Ask the server to reject the subscribe with error 112 when recovery
		// from the provided position is impossible, instead of returning
		// recovered=false — so we can call GetState again to reload state.
		flag |= subscriptionFlagRejectUnrecovered
	}

	// Capture the connection generation as close to the send as possible: the
	// reply is only applied if the client is still in the same connected
	// session when it is processed (see moveToSubscribed). A teardown sneaking
	// between this capture and the send only causes a benign discard-and-retry
	// of an otherwise valid reply.
	connGeneration := s.centrifuge.connGeneration.Load()

	err := s.centrifuge.sendSubscribe(s.Channel, s.data, isRecover, sp, token, s.positioned, s.recoverable, s.joinLeave, s.deltaType, flag, func(res *protocol.SubscribeResult, err error) {
		if err != nil {
			s.inflight.Store(false)
			s.subscribeError(err)
			return
		}
		s.inflight.Store(false)
		s.moveToSubscribed(res, connGeneration)
	})
	if err != nil {
		s.inflight.Store(false)
		s.scheduleResubscribe()
	}
}

func (s *Subscription) getSubscriptionToken(channel string) (string, error) {
	handler := s.getToken
	if handler != nil {
		ev := SubscriptionTokenEvent{
			Channel: channel,
		}
		return handler(ev)
	}
	return "", errors.New("GetToken must be set to get subscription token")
}

// Lock must be held outside.
func (s *Subscription) scheduleSubRefresh(ttl uint32) {
	if s.state != SubStateSubscribed {
		return
	}
	s.refreshTimer = time.AfterFunc(time.Duration(ttl)*time.Second, func() {
		s.mu.Lock()
		if s.state != SubStateSubscribed {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		token, err := s.getSubscriptionToken(s.Channel)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				s.unsubscribe(unsubscribedUnauthorized, "unauthorized", true)
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			s.emitError(SubscriptionRefreshError{Err: err})
			s.scheduleSubRefresh(10)
			return
		}
		if token == "" {
			s.unsubscribe(unsubscribedUnauthorized, "unauthorized", true)
			return
		}

		s.centrifuge.sendSubRefresh(s.Channel, token, func(result *protocol.SubRefreshResult, err error) {
			if err != nil {
				s.emitError(SubscriptionSubscribeError{Err: err})
				var serverError *Error
				if errors.As(err, &serverError) {
					if serverError.Temporary {
						s.mu.Lock()
						defer s.mu.Unlock()
						s.scheduleSubRefresh(10)
						return
					} else {
						s.unsubscribe(serverError.Code, serverError.Message, true)
						return
					}
				} else {
					s.mu.Lock()
					defer s.mu.Unlock()
					s.scheduleSubRefresh(10)
					return
				}
			}
			if result.Expires {
				s.mu.Lock()
				s.scheduleSubRefresh(result.Ttl)
				s.mu.Unlock()
			}
		})
	})
}
