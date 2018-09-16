package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gogo/protobuf/proto"
)

// UnmarshalJSON helps to unmarshal comamnd method when set as string.
func (m *MethodType) UnmarshalJSON(data []byte) error {
	val, err := strconv.Atoi(string(data))
	if err != nil {
		method := strings.Trim(strings.ToUpper(string(data)), `"`)
		if v, ok := MethodType_value[method]; ok {
			*m = MethodType(v)
			return nil
		}
		return err
	}
	*m = MethodType(val)
	return nil
}

// PushDecoder ...
type PushDecoder interface {
	Decode([]byte) (*Push, error)
	DecodePublication([]byte) (*Publication, error)
	DecodeJoin([]byte) (*Join, error)
	DecodeLeave([]byte) (*Leave, error)
	DecodeMessage([]byte) (*Message, error)
	DecodeUnsub([]byte) (*Unsub, error)
}

// JSONPushDecoder ...
type JSONPushDecoder struct {
}

// NewJSONPushDecoder ...
func NewJSONPushDecoder() *JSONPushDecoder {
	return &JSONPushDecoder{}
}

// Decode ...
func (e *JSONPushDecoder) Decode(data []byte) (*Push, error) {
	var m Push
	err := json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodePublication ...
func (e *JSONPushDecoder) DecodePublication(data []byte) (*Publication, error) {
	var m Publication
	err := json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeJoin ...
func (e *JSONPushDecoder) DecodeJoin(data []byte) (*Join, error) {
	var m Join
	err := json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeLeave  ...
func (e *JSONPushDecoder) DecodeLeave(data []byte) (*Leave, error) {
	var m Leave
	err := json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeMessage ...
func (e *JSONPushDecoder) DecodeMessage(data []byte) (*Message, error) {
	var m Message
	err := json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeUnsub ...
func (e *JSONPushDecoder) DecodeUnsub(data []byte) (*Unsub, error) {
	var m Unsub
	err := json.Unmarshal(data, &m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ProtobufPushDecoder ...
type ProtobufPushDecoder struct {
}

// NewProtobufPushDecoder ...
func NewProtobufPushDecoder() *ProtobufPushDecoder {
	return &ProtobufPushDecoder{}
}

// Decode ...
func (e *ProtobufPushDecoder) Decode(data []byte) (*Push, error) {
	var m Push
	err := m.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodePublication ...
func (e *ProtobufPushDecoder) DecodePublication(data []byte) (*Publication, error) {
	var m Publication
	err := m.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeJoin ...
func (e *ProtobufPushDecoder) DecodeJoin(data []byte) (*Join, error) {
	var m Join
	err := m.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeLeave  ...
func (e *ProtobufPushDecoder) DecodeLeave(data []byte) (*Leave, error) {
	var m Leave
	err := m.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeMessage ...
func (e *ProtobufPushDecoder) DecodeMessage(data []byte) (*Message, error) {
	var m Message
	err := m.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// DecodeUnsub ...
func (e *ProtobufPushDecoder) DecodeUnsub(data []byte) (*Unsub, error) {
	var m Unsub
	err := m.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ReplyDecoder ...
type ReplyDecoder interface {
	Reset([]byte) error
	Decode() (*Reply, error)
}

// JSONReplyDecoder ...
type JSONReplyDecoder struct {
	decoder *json.Decoder
}

// NewJSONReplyDecoder ...
func NewJSONReplyDecoder(data []byte) *JSONReplyDecoder {
	return &JSONReplyDecoder{
		decoder: json.NewDecoder(bytes.NewReader(data)),
	}
}

// Reset ...
func (d *JSONReplyDecoder) Reset(data []byte) error {
	d.decoder = json.NewDecoder(bytes.NewReader(data))
	return nil
}

// Decode ...
func (d *JSONReplyDecoder) Decode() (*Reply, error) {
	var c Reply
	err := d.decoder.Decode(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ProtobufReplyDecoder ...
type ProtobufReplyDecoder struct {
	data   []byte
	offset int
}

// NewProtobufReplyDecoder ...
func NewProtobufReplyDecoder(data []byte) *ProtobufReplyDecoder {
	return &ProtobufReplyDecoder{
		data: data,
	}
}

// Reset ...
func (d *ProtobufReplyDecoder) Reset(data []byte) error {
	d.data = data
	d.offset = 0
	return nil
}

// Decode ...
func (d *ProtobufReplyDecoder) Decode() (*Reply, error) {
	if d.offset < len(d.data) {
		var c Reply
		l, n := binary.Uvarint(d.data[d.offset:])
		replyBytes := d.data[d.offset+n : d.offset+n+int(l)]
		err := c.Unmarshal(replyBytes)
		if err != nil {
			return nil, err
		}
		d.offset = d.offset + n + int(l)
		return &c, nil
	}
	return nil, io.EOF
}

// ResultDecoder ...
type ResultDecoder interface {
	Decode([]byte, interface{}) error
}

// JSONResultDecoder ...
type JSONResultDecoder struct{}

// NewJSONResultDecoder ...
func NewJSONResultDecoder() *JSONResultDecoder {
	return &JSONResultDecoder{}
}

// Decode ...
func (e *JSONResultDecoder) Decode(data []byte, dest interface{}) error {
	return json.Unmarshal(data, dest)
}

// ProtobufResultDecoder ...
type ProtobufResultDecoder struct{}

// NewProtobufResultDecoder ...
func NewProtobufResultDecoder() *ProtobufResultDecoder {
	return &ProtobufResultDecoder{}
}

// Decode ...
func (e *ProtobufResultDecoder) Decode(data []byte, dest interface{}) error {
	m, ok := dest.(proto.Unmarshaler)
	if !ok {
		return fmt.Errorf("can not unmarshal type from Protobuf")
	}
	return m.Unmarshal(data)
}
