package centrifuge

// FakeServer is an in-process Centrifugo fake speaking the protobuf protocol
// over a WebSocket. It is intentionally protocol-level and generic: it provides
// sensible defaults for the connect/subscribe/unsubscribe handshake, captures
// received commands for assertions, and exposes hooks + raw push senders so new
// scenarios can be added WITHOUT touching the client under test or this helper.
//
// This exists because some features (channel compaction, and in future others)
// are Centrifugo PRO only and can't be exercised against the OSS docker-compose
// server the other suites use; and because a fake gives deterministic control
// of timing, errors and reconnects.
//
// How to extend (most→least common):
//   - Customize a subscribe reply:       s.OnSubscribe = func(ch string, req *protocol.SubscribeRequest) *protocol.SubscribeResult { return &protocol.SubscribeResult{Recoverable: true, Epoch: "e1"} }
//   - Negotiate channel compaction:      s.OnSubscribe = func(ch string, req *protocol.SubscribeRequest) *protocol.SubscribeResult { r := &protocol.SubscribeResult{}; if req.Flag&1 != 0 { r.Id = 42 }; return r }
//   - Push to a subscription:            s.PublishID(42, data) / s.PublishChannel("news", data)
//   - Fully control any command reply:   s.OnCommand = func(cmd *protocol.Command) *protocol.Reply { ... return nil to fall through }
//   - Send anything the protocol allows: s.SendPush(&protocol.Push{Disconnect: &protocol.Disconnect{Code: 3000}})
//   - Drive a reconnect:                 s.CloseConnection()
//   - Assert on what the client sent:    s.Received() / s.LastSubscribe()

import (
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/centrifugal/protocol"
	"github.com/gorilla/websocket"
)

type FakeServer struct {
	srv *httptest.Server

	mu       sync.Mutex
	current  *websocket.Conn
	received []*protocol.Command

	// ConnectResult is the connect reply. Override to set expires/ttl/data/etc.
	ConnectResult *protocol.ConnectResult
	// OnCommand fully controls the reply to any command — return a reply to
	// send, or nil to fall through to default handling.
	OnCommand func(cmd *protocol.Command) *protocol.Reply
	// OnSubscribe customizes the subscribe result per channel (default: empty).
	OnSubscribe func(channel string, req *protocol.SubscribeRequest) *protocol.SubscribeResult
}

func NewFakeServer(t *testing.T) *FakeServer {
	t.Helper()
	s := &FakeServer{
		ConnectResult: &protocol.ConnectResult{Client: "fake-client", Version: "0.0.0", Ping: 25},
	}
	upgrader := websocket.Upgrader{Subprotocols: []string{"centrifuge-protobuf"}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.current = conn
		s.mu.Unlock()
		s.readLoop(conn)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *FakeServer) URL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/connection/websocket"
}

// Received returns a copy of all commands received from the client, in order.
func (s *FakeServer) Received() []*protocol.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*protocol.Command, len(s.received))
	copy(out, s.received)
	return out
}

// LastSubscribe returns the most recent subscribe request, or nil.
func (s *FakeServer) LastSubscribe() *protocol.SubscribeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.received) - 1; i >= 0; i-- {
		if s.received[i].Subscribe != nil {
			return s.received[i].Subscribe
		}
	}
	return nil
}

// Close stops the server and closes the active connection.
func (s *FakeServer) Close() {
	s.mu.Lock()
	if s.current != nil {
		_ = s.current.Close()
	}
	s.mu.Unlock()
	s.srv.Close()
}

// CloseConnection closes the active connection from the server side, triggering
// the client's automatic reconnect.
func (s *FakeServer) CloseConnection() {
	s.mu.Lock()
	conn := s.current
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *FakeServer) readLoop(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		decoder := protocol.NewProtobufCommandDecoder(data)
		for {
			cmd, err := decoder.Decode()
			if cmd != nil {
				s.dispatch(conn, cmd)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return
			}
		}
	}
}

func (s *FakeServer) dispatch(conn *websocket.Conn, cmd *protocol.Command) {
	s.mu.Lock()
	s.received = append(s.received, cmd)
	s.mu.Unlock()

	if s.OnCommand != nil {
		if reply := s.OnCommand(cmd); reply != nil {
			s.sendReply(conn, reply)
			return
		}
	}

	switch {
	case cmd.Connect != nil:
		s.sendReply(conn, &protocol.Reply{Id: cmd.Id, Connect: s.ConnectResult})
	case cmd.Subscribe != nil:
		result := &protocol.SubscribeResult{}
		if s.OnSubscribe != nil {
			result = s.OnSubscribe(cmd.Subscribe.Channel, cmd.Subscribe)
		}
		s.sendReply(conn, &protocol.Reply{Id: cmd.Id, Subscribe: result})
	case cmd.Unsubscribe != nil:
		s.sendReply(conn, &protocol.Reply{Id: cmd.Id, Unsubscribe: &protocol.UnsubscribeResult{}})
	default:
		if cmd.Id != 0 {
			// Reply to anything else with an empty result to avoid client timeouts.
			s.sendReply(conn, &protocol.Reply{Id: cmd.Id})
		}
	}
}

// --- raw escape hatches -----------------------------------------------------

func (s *FakeServer) sendReply(conn *websocket.Conn, reply *protocol.Reply) {
	raw, err := reply.MarshalVT()
	if err != nil {
		return
	}
	// Protobuf replies are varint length-delimited on the wire (the encoder
	// itself does not add framing — the real server frames via a DataEncoder).
	data := binary.AppendUvarint(make([]byte, 0, len(raw)+8), uint64(len(raw)))
	data = append(data, raw...)
	// Serialize writes: the read loop and test-driven pushes write concurrently
	// and gorilla connections are not safe for concurrent writers.
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = conn.WriteMessage(websocket.BinaryMessage, data)
}

// SendReply sends a raw reply to the active connection.
func (s *FakeServer) SendReply(reply *protocol.Reply) {
	s.mu.Lock()
	conn := s.current
	s.mu.Unlock()
	s.sendReply(conn, reply)
}

// SendPush sends a raw push (wrapped in a reply) to the active connection.
func (s *FakeServer) SendPush(push *protocol.Push) {
	s.SendReply(&protocol.Reply{Push: push})
}

// --- typed push senders -----------------------------------------------------
//
// Channel compaction pushes carry a numeric id and no channel; otherwise the
// channel name is used. The *ID variants address by numeric id, the *Channel
// variants by channel name.

func (s *FakeServer) PublishID(id int64, data []byte) {
	s.SendPush(&protocol.Push{Id: id, Pub: &protocol.Publication{Data: data}})
}

func (s *FakeServer) PublishChannel(channel string, data []byte) {
	s.SendPush(&protocol.Push{Channel: channel, Pub: &protocol.Publication{Data: data}})
}

func (s *FakeServer) JoinID(id int64, client string) {
	s.SendPush(&protocol.Push{Id: id, Join: &protocol.Join{Info: &protocol.ClientInfo{Client: client}}})
}

func (s *FakeServer) LeaveID(id int64, client string) {
	s.SendPush(&protocol.Push{Id: id, Leave: &protocol.Leave{Info: &protocol.ClientInfo{Client: client}}})
}

func (s *FakeServer) UnsubscribePush(channel string, code uint32, reason string) {
	s.SendPush(&protocol.Push{Channel: channel, Unsubscribe: &protocol.Unsubscribe{Code: code, Reason: reason}})
}

func (s *FakeServer) DisconnectPush(code uint32, reason string) {
	s.SendPush(&protocol.Push{Disconnect: &protocol.Disconnect{Code: code, Reason: reason}})
}
