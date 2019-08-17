package main

import (
	"flag"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge-go"
)

var conns int64
var stop int64

func usage() {
	log.Fatalf("Usage: benchmark [-u uri] [-n numConns] channel\n")
}

var url = flag.String("u", "ws://localhost:8000/connection/websocket", "Connection URI")
var connRate = flag.Duration("r", time.Millisecond, "")
var numConns = flag.Int("n", 10000, "")

func main() {

	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		usage()
	}

	go func() {
		for {
			time.Sleep(time.Second)
			fmt.Printf("%d\r", conns)
		}
	}()

	for {
		time.Sleep(*connRate)
		if atomic.LoadInt64(&stop) == 1 {
			continue
		}
		runSubscriber()
	}
}

func newConnection() *centrifuge.Client {
	c := centrifuge.New(*url, centrifuge.DefaultConfig())

	events := &eventHandler{}
	c.OnError(events)
	c.OnDisconnect(events)
	c.OnMessage(events)

	err := c.Connect()
	if err != nil {
		log.Fatalf("Can't connect: %v\n", err)
	}
	return c
}

type eventHandler struct{}

func (h *eventHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) {
}

func (h *eventHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	log.Printf("Disconnected: %s (%t)", e.Reason, e.Reconnect)
}

func (h *eventHandler) OnMessage(c *centrifuge.Client, e centrifuge.MessageEvent) {
}

type subEventHandler struct {
	client *centrifuge.Client
}

func (h *subEventHandler) OnPublish(sub *centrifuge.Subscription, e centrifuge.PublishEvent) {

}

func (h *subEventHandler) OnSubscribeSuccess(sub *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	val := atomic.AddInt64(&conns, 1)
	if int(val) >= *numConns {
		atomic.StoreInt64(&stop, 1)
	}
}

func (h *subEventHandler) OnSubscribeError(sub *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	log.Fatalf("Subscribe error: %v", e.Error)
}

func runSubscriber() {
	c := newConnection()

	args := flag.Args()
	subj := args[0]

	subEvents := &subEventHandler{
		client: c,
	}

	sub, err := c.NewSubscription(subj)
	if err != nil {
		log.Fatalln(err)
	}

	sub.OnPublish(subEvents)
	sub.OnSubscribeSuccess(subEvents)
	sub.OnSubscribeError(subEvents)

	sub.Subscribe()
}
