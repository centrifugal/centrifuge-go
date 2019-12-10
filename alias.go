package centrifuge

import "github.com/centrifugal/protocol"

// Error represents client reply error.
type Error = protocol.Error

// Publication allows to deliver custom payload to all channel subscribers.
type Publication = protocol.Publication

// ClientInfo is short information about client connection.
type ClientInfo = protocol.ClientInfo
