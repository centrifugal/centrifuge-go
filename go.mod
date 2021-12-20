module github.com/centrifugal/centrifuge-go

go 1.13

replace github.com/centrifugal/protocol => ../protocol

require (
	github.com/centrifugal/protocol v0.7.3
	github.com/gorilla/websocket v1.4.2
	github.com/jpillora/backoff v1.0.0
)
