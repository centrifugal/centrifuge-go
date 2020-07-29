package main

import (
	"log"

	"net/http"
	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge-go"
)

type eventHandler struct{}

func (h *eventHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) {
	log.Println("Error", e.Message)
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Println("Disconnected", e.Reason)
}

func (h *eventHandler) OnMessage(c *centrifuge.Client, e centrifuge.MessageEvent) {
	log.Println("Message received", string(e.Data))
	c.RPC([]byte("{}"), func(result centrifuge.RPCResult, err error) {
		if err != nil {
			log.Println(err)
			return
		}
		log.Printf("RPC result 2: %s", string(result.Data))
	})
}

func newConnection() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.New(wsURL, centrifuge.DefaultConfig())
	handler := &eventHandler{}
	c.OnDisconnect(handler)
	c.OnConnect(handler)
	c.OnError(handler)
	c.OnMessage(handler)
	return c
}

func main() {
	log.Println("Start program")
	c := newConnection()
	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	defer func() { _ = c.Close() }()

	go func() {
		log.Println(http.ListenAndServe(":5000", nil))
	}()

	c.RPC([]byte("{}"), func(result centrifuge.RPCResult, err error) {
		if err != nil {
			log.Println(err)
			return
		}
		log.Printf("RPC result: %s", string(result.Data))
	})

	select {}
}
