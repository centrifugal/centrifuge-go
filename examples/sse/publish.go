package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	gocent "github.com/centrifugal/gocent/v3"
)

type post struct {
	number int
}

func publish(ctx context.Context) {
	c := gocent.New(gocent.Config{
		Addr: "http://localhost:8000/api",
		Key:  apiKey,
	})

	i := 0
	for {
		i++
		data, _ := json.Marshal(post{number: i})
		result, err := c.Publish(ctx, channelTest, data)
		if err != nil {
			log.Fatalf("Error calling publish: %v", err)
		}
		log.Printf(
			"Publish into channel %s successful, stream position {offset: %d, epoch: %s}",
			channelTest,
			result.Offset,
			result.Epoch,
		)
		time.Sleep(time.Second)
	}
}
