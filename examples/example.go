package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
	"github.com/centrifugal/centrifugo/libcentrifugo"
	"github.com/centrifugal/centrifugo/libcentrifugo/auth"
)

func main() {
	// Never show secret to client of your application. Keep it on your application backend only.
	secret := "secret"

	// Application user ID.
	user := "42"

	// Current timestamp as string.
	timestamp := centrifuge.Timestamp()

	// Empty info.
	info := ""

	// Generate client token so Centrifugo server can trust connection parameters received from client.
	token := auth.GenerateClientToken(secret, user, timestamp, info)

	creds := &centrifuge.Credentials{
		User:      user,
		Timestamp: timestamp,
		Info:      info,
		Token:     token,
	}

	started := time.Now()

	c := centrifuge.NewCentrifuge("ws://localhost:8000/connection/websocket", creds, centrifuge.DefaultConfig)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	sub, err := c.Subscribe("public:chat")
	if err != nil {
		log.Fatalln(err)
	}

	sub.OnMessage = func(msg libcentrifugo.Message) error {
		log.Println(fmt.Sprintf("New message received in channel %s: %#v", sub.Channel, msg))
		return nil
	}

	sub.OnJoin = func(msg libcentrifugo.ClientInfo) error {
		log.Println(fmt.Sprintf("User %s joined channel %s with client ID %s", msg.User, sub.Channel, msg.Client))
		return nil
	}

	sub.OnLeave = func(msg libcentrifugo.ClientInfo) error {
		log.Println(fmt.Sprintf("User %s with clientID left channel %s with client ID %s", msg.User, msg.Client, sub.Channel))
		return nil
	}

	data := map[string]string{
		"input": "test",
	}
	dataBytes, _ := json.Marshal(data)
	err = sub.Publish(dataBytes)
	if err != nil {
		log.Fatalln(err)
	}

	history, err := sub.History()
	if err != nil {
		log.Fatalln(err)
	}
	log.Printf("%d messages in channel %s history", len(history), sub.Channel)

	presence, err := sub.Presence()
	if err != nil {
		log.Fatalln(err)
	}
	log.Printf("%d clients in channel %s", len(presence), sub.Channel)

	err = sub.Unsubscribe()
	if err != nil {
		log.Fatalln(err)
	}

	c.Close()

	log.Printf("%s", time.Since(started))

}
