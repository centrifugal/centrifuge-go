package centrifuge

import (
	"fmt"

	"github.com/centrifugal/protocol"
)

// Error represents client errors.
type ClientError struct {
	Code    uint32
	Message string
}

// ErrServerConn common error returned on server connection troubles.
type ErrServerConn struct {
	ClientError
}

func errorFromProto(err *protocol.Error) *ClientError {
	return &ClientError{Code: err.Code, Message: err.Message}
}

func (e ClientError) Error() string {
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

func (e ErrServerConn) Error() string {
	return fmt.Sprintf("%d: server connection error: %s", e.Code, e.Message)
}

func newErrServerConn(msg string) ErrServerConn {
	return ErrServerConn{
		ClientError: ClientError{
			Code:    1,
			Message: msg,
		},
	}
}

var (
	// ErrTimeout returned if operation timed out.
	ErrTimeout = ClientError{
		Code:    2,
		Message: "timeout",
	}
	// ErrClientClosed can be returned if client already closed.
	ErrClientClosed = ClientError{
		Code:    3,
		Message: "client closed",
	}
	// ErrClientDisconnected can be returned if client goes to
	// disconnected state while operation in progress.
	ErrClientDisconnected = ClientError{
		Code:    4,
		Message: "client disconnected",
	}
	// ErrReconnectFailed returned when reconnect to server failed (never
	// happen by default since client keeps reconnecting forever).
	ErrReconnectFailed = ClientError{
		Code:    5,
		Message: "reconnect failed",
	}
	// ErrDuplicateSubscription returned if subscription to the same channel
	// already registered in current client instance. This is due to the fact
	// that server does not allow subscribing to the same channel twice for
	// the same connection.
	ErrDuplicateSubscription = ClientError{
		Code:    6,
		Message: "duplicate subscription",
	}
	// ErrSubscribeClosed returned if Subscription was closed.
	ErrSubscriptionClosed = ClientError{
		Code:    7,
		Message: "subscription closed",
	}
)
