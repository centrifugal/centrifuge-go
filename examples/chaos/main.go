package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge-go"
	"github.com/dgrijalva/jwt-go"
)

// In real life clients should never know secret key. This is only for example
// purposes to quickly generate JWT for connection.
const exampleTokenHmacSecret = "secret"

func connToken(user string, exp int64) string {
	// NOTE that JWT must be generated on backend side of your application!
	// Here we are generating it on client side only for example simplicity.
	claims := jwt.MapClaims{"sub": user}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(exampleTokenHmacSecret))
	if err != nil {
		panic(err)
	}
	return t
}

// ChatMessage is chat example specific message struct.
type ChatMessage struct {
	Input string `json:"input"`
}

type eventHandler struct {
	UserID          string
	Channels        []string
	PublishInterval time.Duration
}

func (h *eventHandler) OnConnect(_ *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Printf("Connected to chat with ID %s, user %s", e.ClientID, h.UserID)
}

func (h *eventHandler) OnError(_ *centrifuge.Client, e centrifuge.ErrorEvent) {
	log.Printf("Error: %s, user %s", e.Message, h.UserID)
}

func (h *eventHandler) OnMessage(_ *centrifuge.Client, e centrifuge.MessageEvent) {
	log.Printf("Message from server: %s, user %s", string(e.Data), h.UserID)
}

func (h *eventHandler) OnDisconnect(_ *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Printf("Disconnected from chat: %s, user %s", e.Reason, h.UserID)
}

func (h *eventHandler) OnServerSubscribe(_ *centrifuge.Client, e centrifuge.ServerSubscribeEvent) {
	log.Printf("Subscribe to server-side channel %s: (resubscribe: %t, recovered: %t), user %s", e.Channel, e.Resubscribed, e.Recovered, h.UserID)
}

func (h *eventHandler) OnServerUnsubscribe(_ *centrifuge.Client, e centrifuge.ServerUnsubscribeEvent) {
	log.Printf("Unsubscribe from server-side channel %s, user %s", e.Channel, h.UserID)
}

func (h *eventHandler) OnServerJoin(_ *centrifuge.Client, e centrifuge.ServerJoinEvent) {
	log.Printf("Server-side join to channel %s: %s (%s), user %s", e.Channel, e.User, e.Client, h.UserID)
}

func (h *eventHandler) OnServerLeave(_ *centrifuge.Client, e centrifuge.ServerLeaveEvent) {
	log.Printf("Server-side leave from channel %s: %s (%s), user %s", e.Channel, e.User, e.Client, h.UserID)
}

func (h *eventHandler) OnServerPublish(_ *centrifuge.Client, e centrifuge.ServerPublishEvent) {
	log.Printf("Publication from server-side channel %s: %s, user %s", e.Channel, e.Data, h.UserID)
}

func (h *eventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {
	var chatMessage *ChatMessage
	err := json.Unmarshal(e.Data, &chatMessage)
	if err != nil {
		return
	}
	log.Printf("Someone says via channel %s: %s, user %s", sub.Channel(), chatMessage.Input, h.UserID)
}

func (h *eventHandler) OnJoin(sub *centrifuge.Subscription, e centrifuge.JoinEvent) {
	log.Printf("Someone joined %s: user id %s, client id %s, user %s", sub.Channel(), e.User, e.Client, h.UserID)
}

func (h *eventHandler) OnLeave(sub *centrifuge.Subscription, e centrifuge.LeaveEvent) {
	log.Printf("Someone left %s: user id %s, client id %s, user %s", sub.Channel(), e.User, e.Client, h.UserID)
}

func (h *eventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	log.Printf("Subscribed on channel %s, resubscribed: %v, recovered: %v, user %s", sub.Channel(), e.Resubscribed, e.Recovered, h.UserID)
}

func (h *eventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Printf("Subscribed on channel %s failed, error: %s, user %s", sub.Channel(), e.Error, h.UserID)
}

func (h *eventHandler) OnUnsubscribe(sub *centrifuge.Subscription, _ centrifuge.UnsubscribeEvent) {
	log.Printf("Unsubscribed from channel %s, user %s", sub.Channel(), h.UserID)
}

func newClient(handler *eventHandler) *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.NewJsonClient(wsURL, centrifuge.DefaultConfig())

	c.SetToken(connToken(handler.UserID, 0))

	c.OnConnect(handler)
	c.OnDisconnect(handler)
	c.OnMessage(handler)
	c.OnError(handler)

	c.OnServerPublish(handler)
	c.OnServerSubscribe(handler)
	c.OnServerUnsubscribe(handler)
	c.OnServerJoin(handler)
	c.OnServerLeave(handler)

	for _, ch := range handler.Channels {
		sub, err := c.NewSubscription(ch)
		if err != nil {
			log.Fatalln(err)
		}

		sub.OnPublish(handler)
		sub.OnJoin(handler)
		sub.OnLeave(handler)
		sub.OnSubscribeSuccess(handler)
		sub.OnSubscribeError(handler)
		sub.OnUnsubscribe(handler)

		err = sub.Subscribe()
		if err != nil {
			log.Fatalln(err)
		}

		if handler.PublishInterval > 0 {
			pubText := func(text string) error {
				msg := &ChatMessage{
					Input: text,
				}
				data, _ := json.Marshal(msg)
				_, err := sub.Publish(data)
				return err
			}

			go func() {
				for {
					time.Sleep(handler.PublishInterval)
					err = pubText("hello from user " + handler.UserID)
					if err != nil {
						log.Println(err.Error())
					}
				}
			}()
		}
	}

	return c
}

func runChatClient(id int) *centrifuge.Client {
	handler := &eventHandler{
		UserID:          "user_" + strconv.Itoa(id),
		Channels:        []string{"chat:index"},
		PublishInterval: time.Duration(rand.Intn(3)+3) * time.Second,
	}
	c := newClient(handler)
	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func runChatSpamer(id int) *centrifuge.Client {
	handler := &eventHandler{
		UserID:          "user_" + strconv.Itoa(id),
		Channels:        []string{"chat:index"},
		PublishInterval: time.Second,
	}
	c := newClient(handler)
	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func main() {
	go func() {
		log.Println(http.ListenAndServe(":5000", nil))
	}()

	for i := 1; i < 101; i++ {
		_ = runChatClient(i)
	}

	runChatSpamer(200)

	// Run until CTRL+C.
	select {}
}
