package centrifuge

import (
	"errors"
)

var (
	// ErrTimeout returned if operation timed out.
	ErrTimeout = errors.New("timeout")
	// ErrClientClosed can be returned if client already closed.
	ErrClientClosed = errors.New("client closed")
	// ErrClientDisconnected can be returned if client goes to
	// disconnected state while operation in progress.
	ErrClientDisconnected = errors.New("client disconnected")
	// ErrReconnectFailed returned when reconnect to server failed (never
	// happen by default since client keeps reconnecting forever).
	ErrReconnectFailed = errors.New("reconnect failed")
	// ErrDuplicateSubscription returned if subscription to the same channel
	// already registered in current client instance. This is due to the fact
	// that server does not allow subscribing to the same channel twice for
	// the same connection.
	ErrDuplicateSubscription = errors.New("duplicate subscription")
	// ErrSubscribeClosed returned if Subscription was closed.
	ErrSubscriptionClosed = errors.New("subscription closed")
)
