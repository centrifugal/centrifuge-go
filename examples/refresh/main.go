// Demonstrate how to resque from credentials expiration
// (when connection_lifetime set in Centrifugo).
package main

import (
	"log"
	"time"

	"github.com/centrifugal/centrifuge-go"
	"github.com/golang-jwt/jwt"
)

func connToken(user string, exp int64) string {
	// NOTE that JWT must be generated on backend side of your application!
	// Here we are generating it on client side only for example simplicity.
	claims := jwt.MapClaims{"sub": user}
	if exp > 0 {
		claims["exp"] = exp
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("secret"))
	if err != nil {
		panic(err)
	}
	return t
}

func main() {
	wsURL := "ws://localhost:8000/connection/websocket?cf_protocol_version=v2"
	c := centrifuge.NewJsonClient(wsURL, centrifuge.Config{
		Token: connToken("113", time.Now().Unix()+10),
		// GetConnectionToken will be called to get new connection token when
		// original token set above expires. You can also skip setting initial
		// token – in this case client will call GetConnectionToken before connecting
		// to a server.
		GetConnectionToken: func(_ centrifuge.ConnectionTokenEvent) (string, error) {
			log.Println("Refresh connection token")
			token := connToken("113", time.Now().Unix()+10)
			return token, nil
		},
	})
	defer c.Close()

	c.OnConnected(func(_ centrifuge.ConnectedEvent) {
		log.Println("Connected")
	})
	c.OnConnecting(func(_ centrifuge.ConnectingEvent) {
		log.Println("Connecting")
	})
	c.OnDisconnected(func(_ centrifuge.DisconnectedEvent) {
		log.Println("Disconnected")
	})
	c.OnError(func(e centrifuge.ErrorEvent) {
		log.Println("Error", e.Error.Error())
	})

	err := c.Connect()
	if err != nil {
		log.Fatalln(err)
	}
	select {}
}
