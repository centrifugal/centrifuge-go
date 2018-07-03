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

type eventHandler struct{}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) {
	log.Println("Error", e.Message)
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Println("Disconnected", e.Reason)
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

func newConnection() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	handler := &eventHandler{}

	events := centrifuge.NewEventHub()
	events.OnDisconnect(handler)
	events.OnRefresh(handler)
	events.OnConnect(handler)
	events.OnError(handler)

	c := centrifuge.New(wsURL, events, centrifuge.DefaultConfig())
	c.SetToken(connToken("113", 10))

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
	subEvents.OnPublish(&subEventHandler{})

	_, err := c.Subscribe("chat:index", subEvents)
	if err != nil {
		log.Fatalln(err)
	}

	select {}
}
