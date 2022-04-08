package centrifuge

type code uint32

const (
	disconnectedCodeDisconnectCalled code = 0
	disconnectedCodeUnauthorized     code = 1
	disconnectCodeBadProtocol        code = 2
	disconnectCodeMessageSizeLimit   code = 3
)

const (
	connectingCodeConnectCalled    code = 0
	connectingCodeTransportClosed  code = 1
	connectingCodeNoPing           code = 2
	connectingCodeSubscribeTimeout code = 3
	connectingCodeUnsubscribeError code = 4
	connectingCodeClientSlow       code = 5
)

const (
	subscribingCodeSubscribeCalled    code = 0
	subscribingCodeClientConnecting   code = 1
	subscribingCodeClientDisconnected code = 2
)

const (
	unsubscribedCodeUnsubscribeCalled code = 0
	unsubscribedCodeUnauthorized      code = 1
	unsubscribedCodeClientClosed      code = 2
)
