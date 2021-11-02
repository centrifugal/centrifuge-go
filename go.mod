module github.com/centrifugal/centrifuge-go

go 1.13

replace github.com/lucas-clemente/quic-go v0.24.0 => github.com/alta/quic-go v0.0.0-20210923171602-7151b11990d2

require (
	github.com/centrifugal/protocol v0.7.3
	github.com/gorilla/websocket v1.4.2
	github.com/jpillora/backoff v1.0.0
	github.com/lucas-clemente/quic-go v0.24.0
)
