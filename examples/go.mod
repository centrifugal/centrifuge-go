module github.com/centrifugal/centrifuge-go/examples

go 1.16

replace github.com/centrifugal/centrifuge-go => ../

require (
	github.com/centrifugal/centrifuge-go v0.3.0
	github.com/centrifugal/gocent/v3 v3.2.0
	github.com/centrifugal/protocol v0.10.0
	github.com/golang-jwt/jwt v3.2.2+incompatible
	github.com/r3labs/sse/v2 v2.10.0
	go.uber.org/ratelimit v0.2.0
	gopkg.in/cenkalti/backoff.v1 v1.1.0
)
