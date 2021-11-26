module github.com/centrifugal/centrifuge-go/examples

go 1.14

replace (
	github.com/centrifugal/centrifuge-go => ../
	github.com/lucas-clemente/quic-go v0.24.0 => github.com/alta/quic-go v0.0.0-20210923171602-7151b11990d2
)

require (
	github.com/centrifugal/centrifuge-go v0.3.0
	github.com/golang-jwt/jwt v3.2.2+incompatible
)
