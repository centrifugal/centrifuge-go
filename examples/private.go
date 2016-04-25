package main

// Private subscription example.

import (
	"log"
	// "github.com/centrifugal/centrifuge-go"
	"github.com/shilkin/centrifugo/libcentrifugo"
	"github.com/shilkin/centrifugo/libcentrifugo/auth"
	"gitlab.srv.pv.km/shilkin/centrifuge-go-2"
	"time"
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

func newConnection() centrifuge.Centrifuge {
	creds := credentials()
	wsURL := "ws://localhost:8000/connection/websocket"
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
			return privateSign, nil
		},
		OnDisconnect: centrifuge.DefaultBackoffReconnector,
	}

	c := centrifuge.NewCentrifuge(wsURL, project, creds, events, centrifuge.DefaultConfig)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func main() {
	log.Println("Start program")
	c := newConnection()
	defer c.Close()

	// Subscribe on private channel.
	events := &centrifuge.SubEventHandler{
		OnMessage: func(_ *centrifuge.Sub, msg libcentrifugo.Message) error {
			log.Printf("message: %s", string(*msg.Data))
			return nil
		},
	}
	_, err := c.Subscribe("$1_2", events)
	if err != nil {
		log.Fatalln(err)
	}
	log.Println("Successfully subscribed on private channel, done")

	time.Sleep(90 * time.Second)
	log.Println("Exit")

}
