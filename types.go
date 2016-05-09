package centrifuge

import (
	"encoding/json"
)

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

type JoinLeaveMessage struct {
	Channel string     `json:"channel"`
	Data    ClientInfo `json:"data"`
}

type ConnectClientCommand struct {
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Info      string `json:"info"`
	Token     string `json:"token"`
}

type RefreshClientCommand struct {
	User      string `json:"user"`
	Timestamp string `json:"timestamp"`
	Info      string `json:"info"`
	Token     string `json:"token"`
}

type SubscribeClientCommand struct {
	Channel string `json:"channel"`
	Client  string `json:"client"`
	Last    string `json:"last"`
	Recover bool   `json:"recover"`
	Info    string `json:"info"`
	Sign    string `json:"sign"`
}

type UnsubscribeClientCommand struct {
	Channel string `json:"channel"`
}

type PublishClientCommand struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

type PresenceClientCommand struct {
	Channel string `json:"channel"`
}

type HistoryClientCommand struct {
	Channel string `json:"channel"`
}

type ConnectResponseBody struct {
	Version string `json:"version"`
	Client  string `json:"client"`
	Expires bool   `json:"expires"`
	Expired bool   `json:"expired"`
	TTL     int64  `json:"ttl"`
}

type SubscribeResponseBody struct {
	Channel   string    `json:"channel"`
	Status    bool      `json:"status"`
	Last      string    `json:"last"`
	Messages  []Message `json:"messages"`
	Recovered bool      `json:"recovered"`
}

type UnsubscribeResponseBody struct {
	Channel string `json:"channel"`
	Status  bool   `json:"status"`
}

type PublishResponseBody struct {
	Channel string `json:"channel"`
	Status  bool   `json:"status"`
}

type PresenceResponseBody struct {
	Channel string                `json:"channel"`
	Data    map[string]ClientInfo `json:"data"`
}

type HistoryResponseBody struct {
	Channel string    `json:"channel"`
	Data    []Message `json:"data"`
}

type DisconnectResponseBody struct {
	Reason    string `json:"reason"`
	Reconnect bool   `json:"reconnect"`
}
