module github.com/centrifugal/centrifuge-go/examples

go 1.16

replace github.com/centrifugal/centrifuge-go => ../

require (
	github.com/centrifugal/centrifuge-go v0.3.0
	github.com/golang-jwt/jwt v3.2.2+incompatible
	go.uber.org/ratelimit v0.2.0
)
