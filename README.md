[![GoDoc](https://pkg.go.dev/badge/centrifugal/centrifuge-go)](https://pkg.go.dev/github.com/centrifugal/centrifuge-go)

Websocket client for [Centrifuge](https://github.com/centrifugal/centrifuge) library and [Centrifugo](https://github.com/centrifugal/centrifugo) server.

There is no v1 release of this library yet – API still evolves. At the moment patch version updates only contain backwards compatible changes, minor version updates can have backwards incompatible API changes.

## Feature matrix

- [x] connect to server using JSON protocol format
- [x] connect to server using Protobuf protocol format
- [x] connect with token (JWT)
- [x] connect with custom header
- [x] automatic reconnect in case of errors, network problems etc
- [x] an exponential backoff for reconnect
- [x] connect and disconnect events
- [x] handle disconnect reason
- [x] subscribe on a channel and handle asynchronous Publications
- [x] handle Join and Leave messages
- [x] handle Unsubscribe notifications
- [x] reconnect on subscribe timeout
- [x] publish method of Subscription
- [x] unsubscribe method of Subscription
- [x] presence method of Subscription
- [x] presence stats method of Subscription
- [x] history method of Subscription
- [x] top-level publish method
- [x] top-level presence method
- [x] top-level presence stats method
- [x] top-level history method
- [ ] top-level unsubscribe method
- [x] send asynchronous messages to server
- [x] handle asynchronous messages from server
- [x] send RPC commands
- [x] publish to channel without being subscribed
- [x] subscribe to private channels with token (JWT)
- [x] connection token (JWT) refresh
- [ ] private channel subscription token (JWT) refresh
- [x] handle connection expired error
- [ ] handle subscription expired error
- [x] ping/pong to find broken connection
- [x] message recovery mechanism for client-side subscriptions
- [x] server-side subscriptions
- [x] message recovery mechanism for server-side subscriptions
- [ ] history stream pagination

## Run tests

First run Centrifugo instance:

```
docker run -d -p 8000:8000 centrifugo/centrifugo:latest centrifugo --client_insecure
```

Then run `go test`
