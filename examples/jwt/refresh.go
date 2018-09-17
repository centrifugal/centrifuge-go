// Demonstrate how to resque from credentials expiration
// (when connection_lifetime set in Centrifugo).
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
	claims := jwt.MapClaims{"sub": user}
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

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) {
	log.Println("Error", e.Message)
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Printf("Disconnected, reason: %s, reconnect: %v", e.Reason, e.Reconnect)
}

func (h *eventHandler) OnRefresh(c *centrifuge.Client) (string, error) {
	log.Println("Refresh")
	// TODO: receive connection token.
	token := connToken("113", time.Now().Unix()+10)
	return token, nil
}

type subEventHandler struct{}

func (h *subEventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {
	log.Println(fmt.Sprintf("New message received in channel %s: %s", sub.Channel(), string(e.Data)))
}

func (h *subEventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	log.Println(fmt.Sprintf("Subscribed to channel channel %s: %#v", sub.Channel(), e))
}

func (h *subEventHandler) OnUnsubscribe(sub *centrifuge.Subscription, e centrifuge.UnsubscribeEvent) {
	log.Println(fmt.Sprintf("Unsubscribed from channel channel %s", sub.Channel()))
}

func newConnection() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	handler := &eventHandler{}

	events := centrifuge.NewEventHub()
	events.OnDisconnect(handler)
	events.OnRefresh(handler)
	events.OnConnect(handler)
	events.OnError(handler)

	c := centrifuge.New(wsURL, events, centrifuge.DefaultConfig())
	c.SetToken(connToken("113", time.Now().Unix()+10))

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
	eventHandler := &subEventHandler{}
	subEvents.OnPublish(eventHandler)
	subEvents.OnSubscribeSuccess(eventHandler)
	subEvents.OnUnsubscribe(eventHandler)

	_, err := c.Subscribe("chat:index", subEvents)
	if err != nil {
		log.Fatalln(err)
	}

	select {}
}
