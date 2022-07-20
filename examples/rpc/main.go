package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge-go"
)

func newClient() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.NewJsonClient(wsURL, centrifuge.Config{})

	c.OnConnecting(func(_ centrifuge.ConnectingEvent) {
		log.Println("Connecting")
	})
	c.OnConnected(func(_ centrifuge.ConnectedEvent) {
		log.Println("Connected")
	})
	c.OnDisconnected(func(_ centrifuge.DisconnectedEvent) {
		log.Println("Disconnected")
	})
	c.OnError(func(e centrifuge.ErrorEvent) {
		log.Println("Error", e.Error.Error())
	})
	c.OnMessage(func(e centrifuge.MessageEvent) {
		log.Println("Message received", string(e.Data))

		// When issue blocking requests from inside event handler we must use
		// a goroutine. Otherwise, connection read loop will be blocked.
		go func() {
			result, err := c.RPC(context.Background(), "method", []byte("{}"))
			if err != nil {
				log.Println(err)
				return
			}
			log.Printf("RPC result 2: %s", string(result.Data))
		}()
	})
	return c
}

func main() {
	log.Println("Start program")

	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	c := newClient()
	defer c.Close()

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	result, err := c.RPC(context.Background(), "method", []byte("{}"))
	if err != nil {
		log.Fatalln(err)
		return
	}
	log.Printf("RPC result: %s", string(result.Data))

	select {}
}
