package main

// Demonstrate how to reconnect.

import (
	"fmt"
	"log"
	"time"

	"github.com/shilkin/centrifuge-go"
	"github.com/shilkin/centrifugo/libcentrifugo"
	"github.com/shilkin/centrifugo/libcentrifugo/auth"
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
}

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
	wsURL := "ws://mailtest-7.dev.search.km:8001/connection/websocket"
	project := "notifications"

	events := &centrifuge.EventHandler{
		OnPrivateSub: func(c centrifuge.Centrifuge, req *centrifuge.PrivateRequest) (*centrifuge.PrivateSign, error) {
			// Here we allow everyone to subscribe on private channel.
			// To reject subscription we could return any error from this func.
			// In most real application secret key must not be kept on client side
			// and here must be request to your backend to get channel sign.
			info := ""
			sign := auth.GenerateChannelSign("0", req.ClientID, req.Channel, info)
			privateSign := &centrifuge.PrivateSign{Sign: sign, Info: info}
			return privateSign, fmt.Errorf("error stub")
		},
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

	sub, err := c.Subscribe("$1_1", subEvents)
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
