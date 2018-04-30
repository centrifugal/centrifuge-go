package proto

import "sync"

// Encoding determines connection protocol encoding in use.
type Encoding int

const (
	// EncodingJSON means JSON protocol.
	EncodingJSON Encoding = 0
	// EncodingProtobuf means protobuf protocol.
	EncodingProtobuf Encoding = 1
)

// NewPushEncoder ...
func NewPushEncoder(enc Encoding) PushEncoder {
	if enc == EncodingJSON {
		return NewJSONPushEncoder()
	}
	return NewProtobufPushEncoder()
}

// NewPushDecoder ...
func NewPushDecoder(enc Encoding) PushDecoder {
	if enc == EncodingJSON {
		return NewJSONPushDecoder()
	}
	return NewProtobufPushDecoder()
}

var (
	jsonReplyEncoderPool     sync.Pool
	protobufReplyEncoderPool sync.Pool
)

// GetReplyEncoder ...
func GetReplyEncoder(enc Encoding) ReplyEncoder {
	if enc == EncodingJSON {
		e := jsonReplyEncoderPool.Get()
		if e == nil {
			return NewJSONReplyEncoder()
		}
		encoder := e.(ReplyEncoder)
		encoder.Reset()
		return encoder
	}
	e := protobufReplyEncoderPool.Get()
	if e == nil {
		return NewProtobufReplyEncoder()
	}
	encoder := e.(ReplyEncoder)
	encoder.Reset()
	return encoder
}

// PutReplyEncoder ...
func PutReplyEncoder(enc Encoding, e ReplyEncoder) {
	if enc == EncodingJSON {
		jsonReplyEncoderPool.Put(e)
		return
	}
	protobufReplyEncoderPool.Put(e)
}

// NewReplyDecoder ...
func NewReplyDecoder(enc Encoding, data []byte) ReplyDecoder {
	if enc == EncodingJSON {
		return NewJSONReplyDecoder(data)
	}
	return NewProtobufReplyDecoder(data)
}

// NewResultDecoder ...
func NewResultDecoder(enc Encoding) ResultDecoder {
	if enc == EncodingJSON {
		return NewJSONResultDecoder()
	}
	return NewProtobufResultDecoder()
}

// NewParamsEncoder ...
func NewParamsEncoder(enc Encoding) ParamsEncoder {
	if enc == EncodingJSON {
		return NewJSONParamsEncoder()
	}
	return NewProtobufParamsEncoder()
}

// NewCommandEncoder ...
func NewCommandEncoder(enc Encoding) CommandEncoder {
	if enc == EncodingJSON {
		return NewJSONCommandEncoder()
	}
	return NewProtobufCommandEncoder()
}
