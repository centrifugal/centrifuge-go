package main

// Supposed to run for channel which only have `publish` option enabled.

import (
	"log"

	"github.com/centrifugal/centrifuge-go"
	"github.com/centrifugal/centrifugo/libcentrifugo/auth"
)

func credentials() *centrifuge.Credentials {
	secret := "secret"

	// Application user ID.
	user := "42"

	// Current timestamp as string.
	timestamp := centrifuge.Timestamp()

	// Empty info.
	info := ""

	// Generate client token so Centrifugo server can trust connection parameters received from client.
	token := auth.GenerateClientToken(secret, user, timestamp, info)

	return &centrifuge.Credentials{
		User:      user,
		Timestamp: timestamp,
		Info:      info,
		Token:     token,
	}
}

func newConnection(done chan struct{}) *centrifuge.Centrifuge {
	creds := credentials()
	wsURL := "ws://localhost:8000/connection/websocket"

	events := &centrifuge.EventHandler{
		OnDisconnect: func(c *centrifuge.Centrifuge) error {
			log.Println("disconnected")
			close(done)
			return nil
		},
		OnRefresh: func(c *centrifuge.Centrifuge) (*centrifuge.Credentials, error) {
			log.Println("refresh")
			return credentials(), nil
		},
	}

	c := centrifuge.NewCentrifuge(wsURL, creds, events, centrifuge.DefaultConfig)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func main() {
	log.Println("start program")
	done := make(chan struct{})
	c := newConnection(done)
	defer c.Close()
	<-done
}
