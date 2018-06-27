// Private channel subscription example.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/centrifugal/centrifuge-go"
)

type eventHandler struct{}

func (h *eventHandler) OnPrivateSub(c *centrifuge.Client, e centrifuge.PrivateSubEvent) (centrifuge.PrivateSign, error) {
	// TODO: receive subscription token.
	token := ""
	privateSign := centrifuge.PrivateSign{Token: token}
	return privateSign, nil
}

type subEventHandler struct{}

func (h *subEventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	log.Println(fmt.Sprintf("Successfully subscribed on private channel %s", sub.Channel()))
	os.Exit(0)
}

func (h *subEventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Println(fmt.Sprintf("Error subscribing to private channel %s: %v", sub.Channel(), e.Error))
	os.Exit(1)
}

func newConnection() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	handler := &eventHandler{}
	events := centrifuge.NewEventHub()
	events.OnPrivateSub(handler)

	c := centrifuge.New(wsURL, events, centrifuge.DefaultConfig())
	// TODO: receive connection token.
	c.SetToken("")

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

	// Subscribe on private channel.
	c.Subscribe("$public:chat", subEvents)

	select {}
}
