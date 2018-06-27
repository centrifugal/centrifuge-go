// Demonstrate how to resque from credentials expiration
// (when connection_lifetime set in Centrifugo).
package main

import (
	"fmt"
	"log"

	"github.com/centrifugal/centrifuge-go"
)

type eventHandler struct{}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Println("Disconnected")
}

func (h *eventHandler) OnRefresh(c *centrifuge.Client) (string, error) {
	log.Println("Refresh")
	// TODO: receive connection token.
	token := ""
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

	c := centrifuge.New(wsURL, events, centrifuge.DefaultConfig())
	// TODO: receive connection token.
	c.SetToken("")

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	subEvents := centrifuge.NewSubscriptionEventHub()
	subEvents.OnPublish(&subEventHandler{})

	_, err = c.Subscribe("public:chat", subEvents)
	if err != nil {
		log.Fatalln(err)
	}

	return c
}

func main() {
	log.Println("Start program")
	newConnection()
	select {}
}
