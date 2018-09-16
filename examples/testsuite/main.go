package main

import (
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
)

type eventHandler struct{}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Printf("Connected with id: %s", e.ClientID)
	return
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Printf("Disconnected: %s, reconnect: %v", e.Reason, e.Reconnect)
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

func testCustomHeader() {
	url := "ws://localhost:10000/connection/websocket"
	config := centrifuge.DefaultConfig()
	config.Header.Add("Authorization", "testsuite")

	events := centrifuge.NewEventHub()
	eventHandler := &eventHandler{}
	events.OnConnect(eventHandler)
	events.OnDisconnect(eventHandler)

	c := centrifuge.New(url, events, config)
	defer c.Close()
	err := c.Connect()
	if err != nil {
		panic(err.Error())
	}
}

func testJWTAuth() {
	url := "ws://localhost:10001/connection/websocket"
	config := centrifuge.DefaultConfig()
	events := centrifuge.NewEventHub()
	eventHandler := &eventHandler{}
	events.OnConnect(eventHandler)
	events.OnDisconnect(eventHandler)

	c := centrifuge.New(url, events, config)
	c.SetToken("eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ0ZXN0c3VpdGVfand0IiwiZXhwIjoxNTM0NjY0MjE0fQ.pFyddmlMVYQWmed3dfzuHOViNew9DP2yHjzE20unFb4")
	defer c.Close()
	err := c.Connect()
	if err != nil {
		panic(err.Error())
	}
}

func testSimpleSubscribe() {
	url := "ws://localhost:10002/connection/websocket"
	config := centrifuge.DefaultConfig()
	events := centrifuge.NewEventHub()
	eventHandler := &eventHandler{}
	events.OnConnect(eventHandler)
	events.OnDisconnect(eventHandler)

	c := centrifuge.New(url, events, config)
	defer c.Close()
	err := c.Connect()
	if err != nil {
		panic(err.Error())
	}
	c.Subscribe("testsuite", nil)
	time.Sleep(time.Second)
}

func testReceiveRPCReceiveMessage(protobuf bool) {
	url := "ws://localhost:10003/connection/websocket"
	if protobuf {
		url = "ws://localhost:10004/connection/websocket?format=protobuf"
	}
	config := centrifuge.DefaultConfig()
	events := centrifuge.NewEventHub()
	eventHandler := &eventHandler{}
	events.OnConnect(eventHandler)
	events.OnDisconnect(eventHandler)

	c := centrifuge.New(url, events, config)
	defer c.Close()
	err := c.Connect()
	if err != nil {
		panic(err)
	}
	data, err := c.RPC([]byte("{}"))
	if err != nil {
		panic(err)
	}
	err = c.Send(data)
	if err != nil {
		panic(err)
	}
}

func main() {
	go testCustomHeader()
	go testJWTAuth()
	go testSimpleSubscribe()
	go testReceiveRPCReceiveMessage(false)
	go testReceiveRPCReceiveMessage(true)

	select {}
}
