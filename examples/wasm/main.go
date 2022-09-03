package main

import (
	"log"

	"github.com/centrifugal/centrifuge-go"
)

func main() {
	client := centrifuge.NewJsonClient(
		"ws://localhost:8000/connection/websocket",
		centrifuge.Config{},
	)
	defer client.Close()

	client.OnConnecting(func(e centrifuge.ConnectingEvent) {
		log.Printf("Connecting - %d (%s)", e.Code, e.Reason)
	})
	client.OnConnected(func(e centrifuge.ConnectedEvent) {
		log.Printf("Connected with ID %s", e.ClientID)
	})
	client.OnDisconnected(func(e centrifuge.DisconnectedEvent) {
		log.Printf("Disconnected: %d (%s)", e.Code, e.Reason)
	})

	client.OnError(func(e centrifuge.ErrorEvent) {
		log.Printf("Error: %s", e.Error.Error())
	})

	err := client.Connect()
	if err != nil {
		log.Printf("Connect error: %v", err)
	}

	sub, err := client.NewSubscription("chat:index", centrifuge.SubscriptionConfig{
		Recoverable: true,
		JoinLeave:   true,
	})
	if err != nil {
		log.Printf("Subscription create error: %v", err)
	}

	sub.OnSubscribing(func(e centrifuge.SubscribingEvent) {
		log.Printf("Subscribing on channel %s - %d (%s)", sub.Channel, e.Code, e.Reason)
	})
	sub.OnSubscribed(func(e centrifuge.SubscribedEvent) {
		log.Printf("Subscribed on channel %s, (was recovering: %v, recovered: %v)", sub.Channel, e.WasRecovering, e.Recovered)
	})
	sub.OnUnsubscribed(func(e centrifuge.UnsubscribedEvent) {
		log.Printf("Unsubscribed from channel %s - %d (%s)", sub.Channel, e.Code, e.Reason)
	})
	sub.OnError(func(e centrifuge.SubscriptionErrorEvent) {
		log.Printf("Subscription error %s: %s", sub.Channel, e.Error)
	})
	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		log.Printf("Someone says via channel %s: %s (offset %d)", sub.Channel, e.Data, e.Offset)
	})

	err = sub.Subscribe()
	if err != nil {
		log.Printf("Subscribe error: %v", err)
	}

	select {}
}
