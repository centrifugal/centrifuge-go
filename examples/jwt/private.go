// Private channel subscription example.
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
	jwt "github.com/dgrijalva/jwt-go"
)

func connToken(user string, exp int64) string {
	// NOTE that JWT must be generated on backend side of your application!
	// Here we are generating it on client side only for example simplicity.
	claims := jwt.MapClaims{"user": user}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		panic(err)
	}
	return t
}

func subscribeToken(channel string, client string, exp int64) string {
	// NOTE that JWT must be generated on backend side of your application!
	// Here we are generating it on client side only for example simplicity.
	claims := jwt.MapClaims{"channel": channel, "client": client}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		panic(err)
	}
	return t
}

type eventHandler struct{}

func (h *eventHandler) OnPrivateSub(c *centrifuge.Client, e centrifuge.PrivateSubEvent) (string, error) {
	token := subscribeToken(e.Channel, e.ClientID, time.Now().Unix()+10)
	return token, nil
}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) {
	log.Println("Error", e.Message)
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Println("Disconnected", e.Reason)
}

type subEventHandler struct{}

func (h *subEventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	log.Println(fmt.Sprintf("Successfully subscribed to private channel %s", sub.Channel()))
}

func (h *subEventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Println(fmt.Sprintf("Error subscribing to private channel %s: %v", sub.Channel(), e.Error))
}

func (h *subEventHandler) OnUnsubscribe(sub *centrifuge.Subscription, e centrifuge.UnsubscribeEvent) {
	log.Println(fmt.Sprintf("Unsubscribed from private channel %s", sub.Channel()))
}

func (h *subEventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {
	log.Println(fmt.Sprintf("New message received from channel %s: %s", sub.Channel(), string(e.Data)))
}

func newConnection() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	handler := &eventHandler{}
	events := centrifuge.NewEventHub()
	events.OnPrivateSub(handler)
	events.OnDisconnect(handler)
	events.OnConnect(handler)
	events.OnError(handler)

	c := centrifuge.New(wsURL, events, centrifuge.DefaultConfig())
	c.SetToken(connToken("112", 0))

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func main() {
	log.Println("Start program")
	c := newConnection()
	defer c.Close()

	subEvents := centrifuge.NewSubscriptionEventHub()
	subEventHandler := &subEventHandler{}
	subEvents.OnSubscribeSuccess(subEventHandler)
	subEvents.OnSubscribeError(subEventHandler)
	subEvents.OnUnsubscribe(subEventHandler)
	subEvents.OnPublish(subEventHandler)

	// Subscribe on private channel.
	c.Subscribe("$chat:index", subEvents)

	select {}
}
