package main

// Connect, subscribe on channel, publish into channel, read presence and history info.

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
)

type testMessage struct {
	Input string `json:"input"`
}

type subEventHandler struct{}

func (h *subEventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {
	log.Println(fmt.Sprintf("New publication received from channel %s: %s", sub.Channel(), string(e.Data)))
}

func (h *subEventHandler) OnJoin(sub *centrifuge.Subscription, e centrifuge.JoinEvent) {
	log.Println(fmt.Sprintf("User %s (client ID %s) joined channel %s", e.User, e.Client, sub.Channel()))
}

func (h *subEventHandler) OnLeave(sub *centrifuge.Subscription, e centrifuge.LeaveEvent) {
	log.Println(fmt.Sprintf("User %s (client ID %s) left channel %s", e.User, e.Client, sub.Channel()))
}

func main() {
	started := time.Now()

	url := "ws://localhost:8000/connection/websocket"
	c := centrifuge.New(url, nil, centrifuge.DefaultConfig())
	defer c.Close()

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	events := centrifuge.NewSubscriptionEventHub()
	subEventHandler := &subEventHandler{}
	events.OnPublish(subEventHandler)
	events.OnJoin(subEventHandler)
	events.OnLeave(subEventHandler)

	sub := c.Subscribe("chat:index", events)

	data := testMessage{Input: "example input"}
	dataBytes, _ := json.Marshal(data)
	err = sub.Publish(dataBytes)
	if err != nil {
		log.Fatalln(err)
	}

	history, err := sub.History()
	if err != nil {
		log.Fatalln(err)
	}
	log.Printf("%d messages in channel %s history", len(history), sub.Channel())

	presence, err := sub.Presence()
	if err != nil {
		log.Fatalln(err)
	}
	log.Printf("%d clients in channel %s", len(presence), sub.Channel())

	err = sub.Unsubscribe()
	if err != nil {
		log.Fatalln(err)
	}

	log.Printf("%s", time.Since(started))
}
