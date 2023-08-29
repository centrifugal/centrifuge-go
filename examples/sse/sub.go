package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/centrifugal/protocol"
	"github.com/golang-jwt/jwt"
	sse "github.com/r3labs/sse/v2"
	"gopkg.in/cenkalti/backoff.v1"
)

type BoolValue struct {
	Value bool `json:"value,omitempty"`
}

type SubscribeOptionOverride struct {
	// Presence turns on participating in channel presence.
	Presence *BoolValue `json:"presence,omitempty"`
	// JoinLeave enables sending Join and Leave messages for this client in channel.
	JoinLeave *BoolValue `json:"join_leave,omitempty"`
	// ForcePushJoinLeave forces sending join/leave for this client.
	ForcePushJoinLeave *BoolValue `json:"force_push_join_leave,omitempty"`
	// ForcePositioning on says that client will additionally sync its position inside
	// a stream to prevent message loss. Make sure you are enabling ForcePositioning in channels
	// that maintain Publication history stream. When ForcePositioning is on  Centrifuge will
	// include StreamPosition information to subscribe response - for a client to be able
	// to manually track its position inside a stream.
	ForcePositioning *BoolValue `json:"force_positioning,omitempty"`
	// ForceRecovery turns on recovery option for a channel. In this case client will try to
	// recover missed messages automatically upon resubscribe to a channel after reconnect
	// to a server. This option also enables client position tracking inside a stream
	// (like ForcePositioning option) to prevent occasional message loss. Make sure you are using
	// ForceRecovery in channels that maintain Publication history stream.
	ForceRecovery *BoolValue `json:"force_recovery,omitempty"`
}

type SubscribeOptions struct {
	// Info defines custom channel information, zero value means no channel information.
	Info json.RawMessage `json:"info,omitempty"`
	// Base64Info is like Info but for binary.
	Base64Info string `json:"b64info,omitempty"`
	// Data to send to a client with Subscribe Push.
	Data json.RawMessage `json:"data,omitempty"`
	// Base64Data is like Data but for binary data.
	Base64Data string `json:"b64data,omitempty"`
	// Override channel options can contain channel options overrides.
	Override *SubscribeOptionOverride `json:"override,omitempty"`
}

// subscribe
// SubscribeOptions define per-subscription options.
type Sub map[string]interface{}

func connToken(user string, exp int64) string {
	// https://github.com/centrifugal/centrifugo/blob/5a7b9176b3be622af1a21e134b880e1cc5e4b4e4/internal/jwtverify/token_verifier_jwt.go#L377
	subs := Sub{
		channelTest: SubscribeOptions{},
	}
	claims := jwt.MapClaims{"sub": user, "subs": subs}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(jwtKey))
	if err != nil {
		panic(err)
	}
	return t
}

func sub(ctx context.Context) {
	token := connToken(userid, time.Now().Unix()+3600*24)
	subs := make(map[string]*protocol.SubscribeRequest)
	subs[channelTest] = &protocol.SubscribeRequest{
		// Whether a client wants to recover from a certain position
		Recover: false,
		// Known stream position epoch when recover is used
		Epoch: "",
		// Known stream position offset when recover is used
		Offset: 0,
	}
	req := &protocol.ConnectRequest{
		Name:  "1",
		Token: token,
		Subs:  subs,
	}
	data, err := json.Marshal(&req)
	if err != nil {
		panic(err)
	}

	params := url.Values{}
	params.Add("cf_connect", string(data))

	// https://github.com/centrifugal/centrifugo/blob/0be4d975085ac45d1deaba9bca70d091feced2e9/internal/unisse/handler.go#L34
	sseURL := "http://localhost:8000/connection/uni_sse?" + params.Encode()
	fmt.Println(sseURL)
	client := sse.NewClient(sseURL, sse.ClientMaxBufferSize(1024*1024*2))
	client.Connection.Transport = &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
		Dial: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 10 * time.Second,
		}).Dial,
	}

	retry := backoff.NewExponentialBackOff()
	retry.MaxInterval = 10 * time.Second
	client.ReconnectStrategy = retry
	client.ReconnectNotify = func(err error, next time.Duration) {
		log.Println("Reconnecting after", next, "due to", err)
	}

	client.OnConnect(func(c *sse.Client) {
		log.Println("connected to server")
	})
	client.OnDisconnect(func(c *sse.Client) {
		log.Println("disconnect from server")
	})
	log.Println("start to Subscribe")
	err = client.SubscribeWithContext(context.Background(), "", func(msg *sse.Event) {
		log.Println("received, id:", string(msg.ID), "data:", string(msg.Data), "event:", string(msg.Event))
	})
	if err != nil {
		panic(err)
	}
}
