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

// Server-sent "state invalidated" codes. The server determines that the
// cached state and/or token are no longer valid and asks the client to drop
// them and re-sync.
const (
	// unsubscribedStateInvalidated is sent in an Unsubscribe push for a single
	// subscription. The client clears the subscription token and cached state
	// and resubscribes (it's >= 2500, so resubscribe applies).
	unsubscribedStateInvalidated uint32 = 2502
	// disconnectedStateInvalidated is sent in a Disconnect push for the whole
	// connection. The client clears the connection token (to force getToken),
	// invalidates all subscriptions' state, and reconnects.
	disconnectedStateInvalidated uint32 = 3014
)

// stateInvalidatedEpoch is a sentinel recovery epoch used after state
// invalidation: the client resubscribes with recover=true and this epoch, which
// the server can never match, so the reply reports WasRecovering=true,
// Recovered=false — the application then reloads via its recovery-failure path.
const stateInvalidatedEpoch = "_"
