package main

import (
	"log"
	"net/http"

	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge-go"
)

func newClient() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.NewJsonClient(wsURL, centrifuge.Config{})
	c.OnConnect(func(_ centrifuge.ConnectEvent) {
		log.Println("Connected")
	})
	c.OnDisconnect(func(_ centrifuge.DisconnectEvent) {
		log.Println("Disconnected")
	})
	c.OnError(func(e centrifuge.ErrorEvent) {
		log.Println("Error", e.Error.Error())
	})
	c.OnMessage(func(e centrifuge.MessageEvent) {
		log.Println("Message received", string(e.Data))
		result, err := c.RPC("method", []byte("{}"))
		if err != nil {
			log.Println(err)
			return
		}
		log.Printf("RPC result 2: %s", string(result.Data))
	})
	return c
}

func main() {
	log.Println("Start program")

	c := newClient()
	defer c.Close()

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	go func() {
		log.Println(http.ListenAndServe(":6060", nil))
	}()

	result, err := c.RPC("method", []byte("{}"))
	if err != nil {
		log.Fatalln(err)
		return
	}
	log.Printf("RPC result: %s", string(result.Data))

	select {}
}
