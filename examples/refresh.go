package main

// Demonstrate how to resque from credentials expiration (when connection_lifetime set in Centrifugo).

import (
	"fmt"
	"log"

	"github.com/shilkin/centrifuge-go"
	"github.com/shilkin/centrifugo/libcentrifugo"
	"github.com/shilkin/centrifugo/libcentrifugo/auth"
)

func credentials() *centrifuge.Credentials {
	secret := "0"

	// Project ID
	project := "notifications"

	// Application user ID.
	user := "1"

	// Current timestamp as string.
	timestamp := centrifuge.Timestamp()

	// Empty info.
	info := ""

	// Generate client token so Centrifugo server can trust connection parameters received from client.
	token := auth.GenerateClientToken(secret, project, user, timestamp, info)

	return &centrifuge.Credentials{
		User:      user,
		Timestamp: timestamp,
		Info:      info,
		Token:     token,
	}
}

func newConnection(done chan struct{}) centrifuge.Centrifuge {
	creds := credentials()
	wsURL := "ws://localhost:8000/connection/websocket"
	project := "notifications"

	events := &centrifuge.EventHandler{
		OnDisconnect: func(c centrifuge.Centrifuge) error {
			log.Println("Disconnected")
			close(done)
			return nil
		},
		OnRefresh: func(c centrifuge.Centrifuge) (*centrifuge.Credentials, error) {
			log.Println("Refresh")
			return credentials(), nil
		},
		OnPrivateSub: func(c centrifuge.Centrifuge, req *centrifuge.PrivateRequest) (*centrifuge.PrivateSign, error) {
			// Here we allow everyone to subscribe on private channel.
			// To reject subscription we could return any error from this func.
			// In most real application secret key must not be kept on client side
			// and here must be request to your backend to get channel sign.
			info := ""
			sign := auth.GenerateChannelSign("0", req.ClientID, req.Channel, info)
			privateSign := &centrifuge.PrivateSign{Sign: sign, Info: info}
			return privateSign, nil
		},
	}

	c := centrifuge.NewCentrifuge(wsURL, project, creds, events, centrifuge.DefaultConfig)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	onMessage := func(sub *centrifuge.Sub, msg libcentrifugo.Message) error {
		log.Println(fmt.Sprintf("New message received in channel %s: %#v", sub.Channel, msg))
		return nil
	}

	subEvents := &centrifuge.SubEventHandler{
		OnMessage: onMessage,
	}

	_, err = c.Subscribe("$1_2", subEvents)
	if err != nil {
		log.Fatalln(err)
	}

	return c
}

func main() {
	log.Println("Start program")
	done := make(chan struct{})
	c := newConnection(done)
	defer c.Close()
	<-done
}
