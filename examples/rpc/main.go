package main

import (
	"log"
	"net/http"

	_ "net/http/pprof"

	"github.com/centrifugal/centrifuge-go"
)

type eventHandler struct{}

func (h *eventHandler) OnConnect(_ *centrifuge.Client, _ centrifuge.ConnectEvent) {
	log.Println("Connected")
}

func (h *eventHandler) OnError(_ *centrifuge.Client, err error) {
	log.Println("Error", err.Error())
}

func (h *eventHandler) OnDisconnect(_ *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Println("Disconnected", e.Reason)
}

func (h *eventHandler) OnMessage(c *centrifuge.Client, e centrifuge.MessageEvent) {
	log.Println("Message received", string(e.Data))
	result, err := c.RPC([]byte("{}"))
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("RPC result 2: %s", string(result.Data))
}

func newClient() *centrifuge.Client {
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

	c := newClient()
	defer func() { _ = c.Close() }()

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}

	go func() {
		log.Println(http.ListenAndServe(":5000", nil))
	}()

	result, err := c.RPC([]byte("{}"))
	if err != nil {
		log.Fatalln(err)
		return
	}
	log.Printf("RPC result: %s", string(result.Data))

	select {}
}
