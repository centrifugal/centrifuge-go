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

type eventHandler struct{}

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
	log.Println(fmt.Sprintf("Subscribed on %s: recovered %v, resubscribed %v", sub.Channel(), e.Recovered, e.Resubscribed))
}

func (h *subEventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Println(fmt.Sprintf("Error subscribing on %s: %s", sub.Channel(), e.Error))
}

func (h *subEventHandler) OnUnsubscribe(sub *centrifuge.Subscription, e centrifuge.UnsubscribeEvent) {
	log.Println(fmt.Sprintf("Unsubscribed from %s", sub.Channel()))
}

func newConnection() *centrifuge.Client {
	url := "ws://localhost:8000/connection/websocket?format=protobuf"

	c := centrifuge.New(url, centrifuge.DefaultConfig())

	handler := &eventHandler{}
	c.OnConnect(handler)
	c.OnDisconnect(handler)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	sub, err := c.NewSubscription("chat:index")
	if err != nil {
		log.Fatalln(err)
	}

	subHandler := &subEventHandler{}
	sub.OnPublish(subHandler)
	sub.OnSubscribeSuccess(subHandler)
	sub.OnSubscribeError(subHandler)
	sub.OnUnsubscribe(subHandler)

	err = sub.Subscribe()
	if err != nil {
		log.Fatalln(err)
	}

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
