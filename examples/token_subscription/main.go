// Private channel subscription example.
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
	"github.com/golang-jwt/jwt"
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

func subscriptionToken(channel string, user string, exp int64) string {
	// NOTE that JWT must be generated on backend side of your application!
	// Here we are generating it on client side only for example simplicity.
	claims := jwt.MapClaims{"channel": channel, "sub": user}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		panic(err)
	}
	return t
}

func main() {
	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.NewJsonClient(wsURL, centrifuge.Config{
		Token: connToken("112", 0),
	})
	defer c.Close()

	c.OnConnected(func(_ centrifuge.ConnectedEvent) {
		log.Println("Connected")
	})
	c.OnConnecting(func(_ centrifuge.ConnectingEvent) {
		log.Println("Connecting")
	})
	c.OnDisconnected(func(e centrifuge.DisconnectedEvent) {
		log.Println("Disconnected", e.Reason)
	})
	c.OnError(func(e centrifuge.ErrorEvent) {
		log.Println("Error", e.Error.Error())
	})

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	sub, err := c.NewSubscription("$chat:index", centrifuge.SubscriptionConfig{
		GetToken: func(e centrifuge.SubscriptionTokenEvent) (string, error) {
			log.Println("Getting subscription token")
			token := subscriptionToken(e.Channel, "112", time.Now().Unix()+10)
			return token, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}

	sub.OnSubscribed(func(e centrifuge.SubscribedEvent) {
		log.Println(fmt.Sprintf("Successfully subscribed to private channel %s", sub.Channel))
	})
	sub.OnError(func(e centrifuge.SubscriptionErrorEvent) {
		log.Println(fmt.Sprintf("Error subscribing to private channel %s: %v", sub.Channel, e.Error))
	})
	sub.OnUnsubscribed(func(e centrifuge.UnsubscribedEvent) {
		log.Println(fmt.Sprintf("Unsubscribed from private channel %s", sub.Channel))
	})
	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		log.Println(fmt.Sprintf("New message received from channel %s: %s", sub.Channel, string(e.Data)))
	})

	// Subscribe on private channel.
	_ = sub.Subscribe()

	select {}
}
