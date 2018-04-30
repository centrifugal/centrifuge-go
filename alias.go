package centrifuge

import "github.com/centrifugal/centrifuge-mobile/internal/proto"

// Error represents client reply error.
type Error = proto.Error

// Publication allows to deliver custom payload to all channel subscribers.
type Publication = proto.Publication

// ClientInfo is short information about client connection.
type ClientInfo = proto.ClientInfo
