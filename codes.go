package centrifuge

const (
	disconnectedDisconnectCalled uint32 = 0
	disconnectedUnauthorized     uint32 = 1
	disconnectBadProtocol        uint32 = 2
	disconnectMessageSizeLimit   uint32 = 3
)

const (
	connectingConnectCalled    uint32 = 0
	connectingTransportClosed  uint32 = 1
	connectingNoPing           uint32 = 2
	connectingSubscribeTimeout uint32 = 3
	connectingUnsubscribeError uint32 = 4
)

const (
	subscribingSubscribeCalled uint32 = 0
	subscribingTransportClosed uint32 = 1
)

const (
	unsubscribedUnsubscribeCalled uint32 = 0
	unsubscribedUnauthorized      uint32 = 1
	unsubscribedClientClosed      uint32 = 2
)

// Subscription feature flags — bitmask sent in SubscribeRequest.Flag.
const (
	// subscriptionFlagChannelCompaction offers channel compaction: the server
	// may replace the string channel name with a short numeric ID in
	// subscription pushes (bandwidth optimization). Safe to send
	// unconditionally — servers that don't support or don't allow it ignore
	// the bit and keep sending the full channel name.
	subscriptionFlagChannelCompaction int64 = 1
	subscriptionFlagRejectUnrecovered int64 = 2
)

// Server error code returned when recovery from the provided position is
// impossible (only sent when subscriptionFlagRejectUnrecovered was requested).
const errorCodeUnrecoverablePosition uint32 = 112
