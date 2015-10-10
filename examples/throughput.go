package main

// Supposed to run for channel which only have `publish` option enabled.

import (
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge-go"
	"github.com/centrifugal/centrifugo/libcentrifugo"
	"github.com/centrifugal/centrifugo/libcentrifugo/auth"
)

func newConnection(n int) *centrifuge.Centrifuge {
	secret := "secret"

	// Application user ID.
	user := strconv.Itoa(n)

	// Current timestamp as string.
	timestamp := centrifuge.Timestamp()

	// Empty info.
	info := ""

	// Generate client token so Centrifugo server can trust connection parameters received from client.
	token := auth.GenerateClientToken(secret, user, timestamp, info)

	creds := &centrifuge.Credentials{
		User:      user,
		Timestamp: timestamp,
		Info:      info,
		Token:     token,
	}

	wsURL := "ws://localhost:8000/connection/websocket"
	c := centrifuge.NewCentrifuge(wsURL, creds, nil, centrifuge.DefaultConfig)

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	return c
}

func main() {
	var wg sync.WaitGroup
	done := make(chan struct{})
	numSubscribers := 100
	numPublish := 500
	totalMsg := numPublish * numSubscribers
	wg.Add(numSubscribers)
	var msgReceived int32 = 0

	for i := 0; i < numSubscribers; i++ {
		time.Sleep(time.Millisecond * 10)
		go func(n int) {
			c := newConnection(n)

			events := &centrifuge.SubEventHandler{
				OnMessage: func(sub *centrifuge.Sub, msg libcentrifugo.Message) error {
					val := atomic.AddInt32(&msgReceived, 1)
					go func(currentVal int32) {
						if currentVal == int32(totalMsg) {
							close(done)
						}
					}(val)
					return nil
				},
			}

			_, err := c.Subscribe("test", events)
			if err != nil {
				log.Fatalln(err)
			}
			wg.Done()
			<-done
		}(i)
	}

	wg.Wait()

	c := newConnection(numSubscribers + 1)
	sub, _ := c.Subscribe("test", nil)
	data := map[string]string{"input": "1"}
	dataBytes, _ := json.Marshal(data)

	started := time.Now()
	for i := 0; i < numPublish; i++ {
		sub.Publish(dataBytes)
	}
	<-done
	elapsed := time.Since(started)
	log.Printf("Total clients %d", numSubscribers)
	log.Printf("Total messages %d", totalMsg)
	log.Printf("Elapsed %s", elapsed)
	log.Printf("Msg/sec %d", (1000*totalMsg)/int(elapsed.Nanoseconds()/1000000))

	c.Close()

}
