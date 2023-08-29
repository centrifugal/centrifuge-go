package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var (
	apiKey = "34c49b9b-d96d-4d8d-8ca6-b8afd1e1dec5"
	jwtKey = "32c68c03-7e14-4cfb-8f25-3442d56b22ed"
)

const (
	channelTest = "facts:devops"
	userid      = "1"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	apiKey = os.Getenv("SSE_API_KEY")
	jwtKey = os.Getenv("SSE_JWT_KEY")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go sub(ctx)
	go publish(ctx)

	chSig := make(chan os.Signal, 1)
	signal.Notify(chSig, syscall.SIGHUP, syscall.SIGINT, os.Interrupt, syscall.SIGTERM)
	<-chSig
	log.Println("shutting down...")
}
