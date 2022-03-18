module github.com/centrifugal/centrifuge-go

go 1.13

replace github.com/centrifugal/protocol => ../protocol

require (
	github.com/centrifugal/protocol v0.8.1
	github.com/google/uuid v1.3.0
	github.com/gorilla/websocket v1.4.2
	github.com/jpillora/backoff v1.0.0
)
