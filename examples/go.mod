module github.com/centrifugal/centrifuge-go/examples

go 1.26.0

replace github.com/centrifugal/centrifuge-go => ../

require (
	github.com/centrifugal/centrifuge-go v0.10.4
	github.com/golang-jwt/jwt/v5 v5.2.2
)

require (
	github.com/centrifugal/fdelta v0.0.2 // indirect
	github.com/centrifugal/protocol v0.23.1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
