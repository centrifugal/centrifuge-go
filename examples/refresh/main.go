// Demonstrate how to resque from credentials expiration
// (when connection_lifetime set in Centrifugo).
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
	"github.com/dgrijalva/jwt-go"
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

func (h *eventHandler) OnConnect(_ *centrifuge.Client, _ centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnError(_ *centrifuge.Client, e centrifuge.ErrorEvent) {
	log.Println("Error", e.Message)
}

func (h *eventHandler) OnDisconnect(_ *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Printf("Disconnected, reason: %s, reconnect: %v", e.Reason, e.Reconnect)
}

func (h *eventHandler) OnRefresh(_ *centrifuge.Client) (string, error) {
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

func (h *subEventHandler) OnUnsubscribe(sub *centrifuge.Subscription, _ centrifuge.UnsubscribeEvent) {
	log.Println(fmt.Sprintf("Unsubscribed from channel channel %s", sub.Channel()))
}

func newClient() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	c := centrifuge.NewJsonClient(wsURL, centrifuge.DefaultConfig())
	c.SetToken(connToken("113", time.Now().Unix()+10))

	handler := &eventHandler{}
	c.OnDisconnect(handler)
	c.OnRefresh(handler)
	c.OnConnect(handler)
	c.OnError(handler)

	return c
}

func main() {
	log.Println("Start program")
	c := newClient()
	defer func() { _ = c.Close() }()

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	sub, err := c.NewSubscription("chat:index")
	if err != nil {
		log.Fatalln(err)
	}

	eventHandler := &subEventHandler{}
	sub.OnPublish(eventHandler)
	sub.OnSubscribeSuccess(eventHandler)
	sub.OnUnsubscribe(eventHandler)

	err = sub.Subscribe()
	if err != nil {
		log.Fatalln(err)
	}

	select {}
}
