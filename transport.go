package centrifuge

import (
	"time"

	"github.com/centrifugal/centrifuge-mobile/internal/proto"
)

type transport interface {
	// Read should read new Reply messages from connection.
	// It should not be thread-safe as we will call it from one goroutine.
	Read() (*proto.Reply, *disconnect, error)
	// Write should write Command to connection with specified write timeout.
	// It should not be thread-safe as we will call it from one goroutine.
	Write(*proto.Command, time.Duration) error
	// Close should close connection and do all clean ups required.
	// It must be safe to call Close several times and concurrently with Read
	// and Write methods.
	Close() error
}
