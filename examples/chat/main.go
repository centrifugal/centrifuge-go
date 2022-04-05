package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"os"

	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge-go"
	"github.com/golang-jwt/jwt"
)

// In real life clients should never know secret key. This is only for example
// purposes to quickly generate JWT for connection.
const exampleTokenHmacSecret = "secret"

func connToken(user string, exp int64) string {
	// NOTE that JWT must be generated on backend side of your application!
	// Here we are generating it on client side only for example simplicity.
	claims := jwt.MapClaims{"sub": user}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(exampleTokenHmacSecret))
	if err != nil {
		panic(err)
	}
	return t
}

// ChatMessage is chat example specific message struct.
type ChatMessage struct {
	Input string `json:"input"`
}

func newClient() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket?cf_protocol_version=v2"
	c := centrifuge.NewJsonClient(wsURL, centrifuge.Config{
		// Uncomment to make it work with Centrifugo JWT auth.
		//Token: connToken("49", 0),
	})

	c.OnConnect(func(e centrifuge.ConnectEvent) {
		log.Printf("Connected to chat with ID %s", e.ClientID)
	})
	c.OnDisconnect(func(e centrifuge.DisconnectEvent) {
		log.Printf("Disconnected from chat: %s", e.Reason)
	})

	c.OnFail(func(e centrifuge.FailEvent) {
		log.Printf("Client failed: %s", e.Reason)
	})

	c.OnError(func(e centrifuge.ErrorEvent) {
		log.Printf("Error: %s", e.Error.Error())
	})

	c.OnMessage(func(e centrifuge.MessageEvent) {
		log.Printf("Message from server: %s", string(e.Data))
	})

	c.OnServerPublication(func(e centrifuge.ServerPublicationEvent) {
		log.Printf("Publication from server-side channel %s: %s", e.Channel, e.Data)
	})
	c.OnServerSubscribe(func(e centrifuge.ServerSubscribeEvent) {
		log.Printf("Subscribe to server-side channel %s: (data: %v)", e.Channel, string(e.Data))
	})
	c.OnServerUnsubscribe(func(e centrifuge.ServerUnsubscribeEvent) {
		log.Printf("Unsubscribe from server-side channel %s", e.Channel)
	})
	c.OnServerJoin(func(e centrifuge.ServerJoinEvent) {
		log.Printf("Server-side join to channel %s: %s (%s)", e.Channel, e.User, e.Client)
	})
	c.OnServerLeave(func(e centrifuge.ServerLeaveEvent) {
		log.Printf("Server-side leave from channel %s: %s (%s)", e.Channel, e.User, e.Client)
	})

	return c
}

func main() {
	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	c := newClient()
	defer c.Close()

	sub, err := c.NewSubscription("chat:index")
	if err != nil {
		log.Fatalln(err)
	}

	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		var chatMessage *ChatMessage
		err := json.Unmarshal(e.Data, &chatMessage)
		if err != nil {
			return
		}
		log.Printf("Someone says via channel %s: %s", sub.Channel, chatMessage.Input)
	})
	sub.OnJoin(func(e centrifuge.JoinEvent) {
		log.Printf("Someone joined %s: user id %s, client id %s", sub.Channel, e.User, e.Client)
	})
	sub.OnLeave(func(e centrifuge.LeaveEvent) {
		log.Printf("Someone left %s: user id %s, client id %s", sub.Channel, e.User, e.Client)
	})
	sub.OnSubscribe(func(e centrifuge.SubscribeEvent) {
		log.Printf("Subscribed on channel %s, data: %v", sub.Channel, string(e.Data))
	})
	sub.OnError(func(e centrifuge.SubscriptionErrorEvent) {
		log.Printf("Subscription error %s: %s", sub.Channel, e.Error)
	})
	sub.OnUnsubscribe(func(_ centrifuge.UnsubscribeEvent) {
		log.Printf("Unsubscribed from channel %s", sub.Channel)
	})
	sub.OnFail(func(e centrifuge.SubscriptionFailEvent) {
		log.Printf("Subscription failed: %s - %s", sub.Channel, e.Reason)
	})

	err = sub.Subscribe()
	if err != nil {
		log.Fatalln(err)
	}

	pubText := func(text string) error {
		msg := &ChatMessage{
			Input: text,
		}
		data, _ := json.Marshal(msg)
		_, err := sub.Publish(data)
		return err
	}

	_ = c.Connect()
	//if err != nil {
	//	log.Fatalln(err)
	//}

	err = pubText("hello")
	if err != nil {
		log.Printf("Error publish: %s", err)
	}

	log.Printf("Print something and press ENTER to send\n")

	// Read input from stdin.
	go func(sub *centrifuge.Subscription) {
		reader := bufio.NewReader(os.Stdin)
		for {
			text, _ := reader.ReadString('\n')
			err = pubText(text)
			if err != nil {
				log.Printf("Error publish: %s", err)
			}
		}
	}(sub)

	// Run until CTRL+C.
	select {}
}
