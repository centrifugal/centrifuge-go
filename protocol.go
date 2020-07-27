package centrifuge

import "github.com/centrifugal/protocol"

func newPushEncoder(enc protocol.Type) protocol.PushEncoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONPushEncoder()
	}
	return protocol.NewProtobufPushEncoder()
}

func newPushDecoder(enc protocol.Type) protocol.PushDecoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONPushDecoder()
	}
	return protocol.NewProtobufPushDecoder()
}

func newReplyDecoder(enc protocol.Type, data []byte) protocol.ReplyDecoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONReplyDecoder(data)
	}
	return protocol.NewProtobufReplyDecoder(data)
}

func newResultDecoder(enc protocol.Type) protocol.ResultDecoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONResultDecoder()
	}
	return protocol.NewProtobufResultDecoder()
}

func newParamsEncoder(enc protocol.Type) protocol.ParamsEncoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONParamsEncoder()
	}
	return protocol.NewProtobufParamsEncoder()
}

func newCommandEncoder(enc protocol.Type) protocol.CommandEncoder {
	if enc == protocol.TypeJSON {
		return protocol.NewJSONCommandEncoder()
	}
	return protocol.NewProtobufCommandEncoder()
}
