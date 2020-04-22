package main

import (
	"log"

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
	// Need separate goroutine or WebSocket read loop will be blocked.
	go func() {
		res, err := c.RPC([]byte("{}"))
		if err != nil {
			log.Println(err)
			return
		}
		log.Printf("RPC result: %s", string(res))
	}()
}

func newConnection() *centrifuge.Client {
	wsURL := "ws://localhost:8000/connection/websocket"

	c := centrifuge.New(wsURL, centrifuge.DefaultConfig())

	handler := &eventHandler{}
	c.OnDisconnect(handler)
	c.OnConnect(handler)
	c.OnError(handler)
	c.OnMessage(handler)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func main() {
	log.Println("Start program")
	c := newConnection()
	defer func() { _ = c.Close() }()

	res, err := c.RPC([]byte("{}"))
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("RPC result: %s", string(res))

	select {}
}
