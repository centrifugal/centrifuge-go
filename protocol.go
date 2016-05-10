package centrifuge

import (
	"encoding/json"
)

type clientCommand struct {
	UID    string      `json:"uid"`
	Method string      `json:"method"`
	Params interface{} `json:"params"`
}

type response struct {
	UID    string          `json:"uid,omitempty"`
	Error  string          `json:"error"`
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body"`
}

type ClientInfo struct {
	User        string           `json:"user"`
	Client      string           `json:"client"`
	DefaultInfo *json.RawMessage `json:"default_info,omitempty"`
	ChannelInfo *json.RawMessage `json:"channel_info,omitempty"`
}

type Message struct {
	UID       string           `json:"uid"`
	Timestamp string           `json:"timestamp"`
	Info      *ClientInfo      `json:"info,omitempty"`
	Channel   string           `json:"channel"`
	Data      *json.RawMessage `json:"data"`
	Client    string           `json:"client,omitempty"`
}

type joinLeaveMessage struct {
	Channel string     `json:"channel"`
	Data    ClientInfo `json:"data"`
}

type connectClientCommand struct {
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Info      string `json:"info"`
	Token     string `json:"token"`
}

type refreshClientCommand struct {
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Info      string `json:"info"`
	Token     string `json:"token"`
}

type subscribeClientCommand struct {
	Channel string `json:"channel"`
	Client  string `json:"client"`
	Last    string `json:"last"`
	Recover bool   `json:"recover"`
	Info    string `json:"info"`
	Sign    string `json:"sign"`
}

type unsubscribeClientCommand struct {
	Channel string `json:"channel"`
}

type publishClientCommand struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

type presenceClientCommand struct {
	Channel string `json:"channel"`
}

type historyClientCommand struct {
	Channel string `json:"channel"`
}

type connectResponseBody struct {
	Version string `json:"version"`
	Client  string `json:"client"`
	Expires bool   `json:"expires"`
	Expired bool   `json:"expired"`
	TTL     int64  `json:"ttl"`
}

type subscribeResponseBody struct {
	Channel   string    `json:"channel"`
	Status    bool      `json:"status"`
	Last      string    `json:"last"`
	Messages  []Message `json:"messages"`
	Recovered bool      `json:"recovered"`
}

type unsubscribeResponseBody struct {
	Channel string `json:"channel"`
	Status  bool   `json:"status"`
}

type publishResponseBody struct {
	Channel string `json:"channel"`
	Status  bool   `json:"status"`
}

type presenceResponseBody struct {
	Channel string                `json:"channel"`
	Data    map[string]ClientInfo `json:"data"`
}

type historyResponseBody struct {
	Channel string    `json:"channel"`
	Data    []Message `json:"data"`
}

type disconnectResponseBody struct {
	Reason    string `json:"reason"`
	Reconnect bool   `json:"reconnect"`
}
