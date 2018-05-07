// Simple Go chat client for https://github.com/centrifugal/centrifuge/tree/master/examples/events example.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/centrifugal/centrifuge-go"
)

// ChatMessage is chat app specific message struct.
type ChatMessage struct {
	Input string `json:"input"`
}

type eventHandler struct {
	out io.Writer
}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Connected to chat with ID %s", e.ClientID))
	return
}

func (h *eventHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Error: %s", e.Message))
	return
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Disconnected from chat: %s", e.Reason))
	return
}

func (h *eventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {
	var chatMessage *ChatMessage
	err := json.Unmarshal(e.Data, &chatMessage)
	if err != nil {
		return
	}
	rePrefix := "Someone says:"
	fmt.Fprintln(h.out, rePrefix, chatMessage.Input)
}

func (h *eventHandler) OnJoin(sub *centrifuge.Subscription, e centrifuge.JoinEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Someone joined: user id %s, client id %s", e.User, e.Client))
}

func (h *eventHandler) OnLeave(sub *centrifuge.Subscription, e centrifuge.LeaveEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Someone left: user id %s, client id %s", e.User, e.Client))
}

func (h *eventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Subscribed on channel %s", sub.Channel()))
}

func (h *eventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Subscribed on channel %s failed, error: %s", sub.Channel(), e.Error))
}

func (h *eventHandler) OnUnsubscribe(sub *centrifuge.Subscription, e centrifuge.UnsubscribeEvent) {
	fmt.Fprintln(h.out, fmt.Sprintf("Unsubscribed from channel %s", sub.Channel()))
}

func main() {
	url := "ws://localhost:8000/connection/websocket?format=protobuf"
	//url := "grpc://localhost:8001"

	fmt.Fprintf(os.Stdout, "Connect to %s\n", url)
	fmt.Fprintf(os.Stdout, "Print something and press ENTER to send\n")

	handler := &eventHandler{os.Stdout}

	events := centrifuge.NewEventHub()
	events.OnConnect(handler)
	events.OnError(handler)
	events.OnDisconnect(handler)

	c := centrifuge.New(url, events, centrifuge.DefaultConfig())

	subEvents := centrifuge.NewSubscriptionEventHub()
	subEvents.OnPublish(handler)
	subEvents.OnJoin(handler)
	subEvents.OnLeave(handler)
	subEvents.OnSubscribeSuccess(handler)
	subEvents.OnSubscribeError(handler)
	subEvents.OnUnsubscribe(handler)

	sub, err := c.Subscribe("chat:index", subEvents)
	if err != nil {
		log.Fatalln(err)
	}

	err = c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	// Read input from stdin.
	go func(sub *centrifuge.Subscription) {
		reader := bufio.NewReader(os.Stdin)
		for {
			text, _ := reader.ReadString('\n')
			msg := &ChatMessage{
				Input: text,
			}
			data, _ := json.Marshal(msg)
			sub.Publish(data)
		}
	}(sub)

	// Run until CTRL+C.
	select {}
}
