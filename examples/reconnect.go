package main

// Demonstrate how to reconnect.

import (
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
	"github.com/centrifugal/centrifugo/libcentrifugo/auth"
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

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

func newConnection(done chan struct{}) centrifuge.Centrifuge {
	creds := credentials()
	wsURL := "ws://localhost:8000/connection/websocket"

	events := &centrifuge.EventHandler{
		OnDisconnect: func(c centrifuge.Centrifuge) error {
			log.Println("Disconnected")
			err := c.Reconnect(centrifuge.DefaultBackoffReconnect)
			if err != nil {
				log.Println(err)
				close(done)
			} else {
				log.Println("Reconnected")
			}
			return nil
		},
	}

	c := centrifuge.NewCentrifuge(wsURL, creds, events, centrifuge.DefaultConfig)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	onMessage := func(sub centrifuge.Sub, msg centrifuge.Message) error {
		log.Println(fmt.Sprintf("New message received in channel %s: %#v", sub.Channel(), msg))
		return nil
	}

	subEvents := &centrifuge.SubEventHandler{
		OnMessage: onMessage,
	}

	sub, err := c.Subscribe("public:chat", subEvents)
	if err != nil {
		log.Fatalln(err)
	}

	go func() {
		for {
			msgs, err := sub.History()
			if err != nil {
				log.Printf("Error retreiving channel history: %s", err.Error())
			} else {
				log.Printf("%d messages in channel history", len(msgs))
			}
			time.Sleep(time.Second)
		}
	}()

	return c
}

func main() {
	log.Println("Start program")
	done := make(chan struct{})
	c := newConnection(done)
	defer c.Close()
	<-done
}
