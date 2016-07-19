package main

// Connect, subscribe on channel, publish into channel, read presence and history info.

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/shilkin/centrifuge-go"
	"github.com/shilkin/centrifugo/libcentrifugo"
	"github.com/shilkin/centrifugo/libcentrifugo/auth"
)

func main() {
	// Never show secret to client of your application. Keep it on your application backend only.
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

	creds := &centrifuge.Credentials{
		User:      user,
		Timestamp: timestamp,
		Info:      info,
		Token:     token,
	}

	started := time.Now()

	isError := false
	events := &centrifuge.EventHandler{
		OnPrivateSub: func(c centrifuge.Centrifuge, req *centrifuge.PrivateRequest) (*centrifuge.PrivateSign, error) {
			// Here we allow everyone to subscribe on private channel.
			// To reject subscription we could return any error from this func.
			// In most real application secret key must not be kept on client side
			// and here must be request to your backend to get channel sign.
			info := ""
			sign := auth.GenerateChannelSign("0", req.ClientID, req.Channel, info)
			privateSign := &centrifuge.PrivateSign{Sign: sign, Info: info}
			if isError {
				// comment this scope if you don't need to test subscription fail
				log.Printf("OnPrivateSub: subscriptoin failed with error stub")
				return privateSign, fmt.Errorf("error stub")
			}
			isError = true
			return privateSign, nil
		},
		OnDisconnect: centrifuge.DefaultBackoffReconnector,
	}

	// wsURL := "ws://mailtest-7.dev.search.km:8001/connection/websocket"
	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.NewCentrifuge(wsURL, project, creds, events, centrifuge.DefaultConfig)
	defer c.Close()

	err := c.Connect()
	if err != nil {
		log.Fatalln("connect: ", err)
	}

	onMessage := func(sub *centrifuge.Sub, msg libcentrifugo.Message) error {
		log.Println(fmt.Sprintf("New message received in channel %s: %#v, %v", sub.Channel, msg, string(*msg.Data)))
		return nil
	}

	onJoin := func(sub *centrifuge.Sub, msg libcentrifugo.ClientInfo) error {
		log.Println(fmt.Sprintf("User %s joined channel %s with client ID %s", msg.User, sub.Channel, msg.Client))
		return nil
	}

	onLeave := func(sub *centrifuge.Sub, msg libcentrifugo.ClientInfo) error {
		log.Println(fmt.Sprintf("User %s with clientID left channel %s with client ID %s", msg.User, msg.Client, sub.Channel))
		return nil
	}

	subEvents := &centrifuge.SubEventHandler{
		OnMessage: onMessage,
		OnJoin:    onJoin,
		OnLeave:   onLeave,
	}

	sub, err := c.Subscribe("$1_0", subEvents)
	if err != nil {
		log.Fatalln("subscribe: ", err)
	}

	time.Sleep(120 * time.Second)

	data := map[string]string{
		"input": "test",
	}
	dataBytes, _ := json.Marshal(data)
	err = sub.Publish(dataBytes)
	if err != nil {
		log.Fatalln("publish: ", err)
	}

	history, err := sub.History()
	if err != nil {
		log.Fatalln("hostory: ", err)
	}
	log.Printf("%d messages in channel %s history", len(history), sub.Channel)

	presence, err := sub.Presence()
	if err != nil {
		log.Fatalln("presence: ", err)
	}
	log.Printf("%d clients in channel %s", len(presence), sub.Channel)

	err = sub.Unsubscribe()
	if err != nil {
		log.Fatalln(err)
	}

	log.Printf("%s", time.Since(started))

}
