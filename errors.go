package centrifuge

import (
	"errors"
	"fmt"
)

var (
	// ErrTimeout returned if operation timed out.
	ErrTimeout = errors.New("timeout")
	// ErrClientDisconnected can be returned if client goes to
	// disconnected state while operation in progress.
	ErrClientDisconnected = errors.New("client disconnected")
	// ErrClientFailed can be returned if client is failed.
	ErrClientFailed = errors.New("client failed")
	// ErrSubscriptionUnsubscribed returned if Subscription is unsubscribed.
	ErrSubscriptionUnsubscribed = errors.New("subscription unsubscribed")
	// ErrSubscriptionFailed returned if Subscription is failed.
	ErrSubscriptionFailed = errors.New("subscription failed")
	// ErrDuplicateSubscription returned if subscription to the same channel
	// already registered in current client instance. This is due to the fact
	// that server does not allow subscribing to the same channel twice for
	// the same connection.
	ErrDuplicateSubscription = errors.New("duplicate subscription")
)

type TransportError struct {
	Err error
}

func (t TransportError) Error() string {
	return fmt.Sprintf("transport error: %v", t.Err)
}

type ConnectError struct {
	Err error
}

func (c ConnectError) Error() string {
	return fmt.Sprintf("connect error: %v", c.Err)
}

type RefreshError struct {
	Err error
}

func (r RefreshError) Error() string {
	return fmt.Sprintf("refresh error: %v", r.Err)
}

type SubscriptionSubscribeError struct {
	Err error
}

func (s SubscriptionSubscribeError) Error() string {
	return fmt.Sprintf("subscribe error: %v", s.Err)
}

type SubscriptionRefreshError struct {
	Err error
}

func (s SubscriptionRefreshError) Error() string {
	return fmt.Sprintf("refresh error: %v", s.Err)
}
