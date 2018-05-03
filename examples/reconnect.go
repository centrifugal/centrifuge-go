package main

// Demonstrate how to reconnect.

import (
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime)
}

type eventHandler struct {
}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Printf("Connected with id: %s", e.ClientID)
	return
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Printf("Disconnected: %s", e.Reason)
	return
}

type subEventHandler struct{}

func (h *subEventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {
	log.Println(fmt.Sprintf("New message received in channel %s: %s", sub.Channel(), string(e.Data)))
}

func (h *subEventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	log.Println(fmt.Sprintf("Subscribed on %s: recovered %s, resubscribed %s", sub.Channel(), e.Recovered, e.Resubscribed))
}

func (h *subEventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Println(fmt.Sprintf("Error subscribing on %s: %s", sub.Channel(), e.Error))
}

func (h *subEventHandler) OnUnsubscribe(sub *centrifuge.Subscription, e centrifuge.UnsubscribeEvent) {
	log.Println(fmt.Sprintf("Unsubscribed from %s", sub.Channel()))
}

func newConnection() *centrifuge.Client {
	url := "ws://localhost:8000/connection/websocket?format=protobuf"

	handler := &eventHandler{}

	events := centrifuge.NewEventHub()
	events.OnConnect(handler)
	events.OnDisconnect(handler)

	c := centrifuge.New(url, events, centrifuge.DefaultConfig())

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	subEvents := centrifuge.NewSubscriptionEventHub()
	subHandler := &subEventHandler{}
	subEvents.OnPublish(subHandler)
	subEvents.OnSubscribeSuccess(subHandler)
	subEvents.OnSubscribeError(subHandler)
	subEvents.OnUnsubscribe(subHandler)

	sub := c.Subscribe("public:chat", subEvents)

	go func() {
		for {
			history, err := sub.History()
			if err != nil {
				log.Printf("Error retreiving channel history: %s", err.Error())
			} else {
				log.Printf("%d messages in channel history", len(history))
			}
			time.Sleep(time.Second)
		}
	}()

	return c
}

func main() {
	log.Println("Start program")
	newConnection()
	select {}
}
