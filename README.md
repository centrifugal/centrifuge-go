[![GoDoc](https://pkg.go.dev/badge/centrifugal/centrifuge-go)](https://pkg.go.dev/github.com/centrifugal/centrifuge-go)

Websocket client for [Centrifuge](https://github.com/centrifugal/centrifuge) library and [Centrifugo](https://github.com/centrifugal/centrifugo) server.

There is no v1 release of this library yet – API still evolves. At the moment patch version updates only contain backwards compatible changes, minor version updates can have backwards incompatible API changes.

## Feature matrix

- [x] connect to server (both JSON and Protobuf supported)
- [x] connect with token (JWT)
- [x] connect with custom header
- [x] connect with custom data
- [x] automatic reconnect in case of errors, network problems etc
- [x] an exponential backoff for reconnect
- [x] client connection state events (connecting, connected, disconnected)
- [x] receive client-scope asynchronous messages
- [x] send asynchronous messages to server
- [x] client request-response methods: rpc, publish, presence, presence stats, history
- [x] subscribe to a channel
- [x] subscription state events (subscribing, subscribed, unsubscribed)
- [x] subscription asynchronous messages
- [x] subscription methods: publish, presence, presence stats, history
- [x] reconnect on subscribe timeout and unsubscribe error
- [x] subscribe to private channels with token (JWT)
- [x] connection token (JWT) refresh
- [x] handle connection expiration
- [x] private channel subscription token (JWT) refresh
- [x] handle subscription expiration
- [x] ping/pong to find broken connection
- [x] message recovery mechanism for client-side subscriptions
- [x] server-side subscriptions
- [x] message recovery mechanism for server-side subscriptions

## Run tests

First run Centrifugo instance:

```
docker run -d -p 8000:8000 centrifugo/centrifugo:latest centrifugo --client_insecure
```

Then run `go test`
