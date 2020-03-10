Websocket client for [Centrifuge](https://github.com/centrifugal/centrifuge) library and [Centrifugo](https://github.com/centrifugal/centrifugo) server.

Documentation
-------------

[API documentation on Godoc](https://godoc.org/github.com/centrifugal/centrifuge-go)

There is no v1 release of this library yet, API is not stable – so use with tools like `dep` or `go mod`.

Feature matrix
--------------

- [x] connect to server using JSON protocol format
- [x] connect to server using Protobuf protocol format
- [x] connect with JWT
- [x] connect with custom header
- [x] automatic reconnect in case of errors, network problems etc
- [x] exponential backoff for reconnect
- [x] connect and disconnect events
- [x] handle disconnect reason
- [x] subscribe on channel and handle asynchronous Publications
- [x] handle Join and Leave messages
- [x] handle Unsubscribe notifications
- [x] reconnect on subscribe timeout
- [x] publish method of Subscription
- [x] unsubscribe method of Subscription
- [x] presence method of Subscription
- [x] presence stats method of Subscription
- [x] history method of Subscription
- [x] send asynchronous messages to server
- [x] handle asynchronous messages from server
- [x] send RPC commands
- [x] publish to channel without being subscribed
- [x] subscribe to private channels with JWT
- [x] connection JWT refresh
- [ ] private channel subscription JWT refresh
- [x] handle connection expired error
- [ ] handle subscription expired error
- [x] ping/pong to find broken connection
- [ ] server-side subscriptions
- [x] message recovery mechanism

License
-------

MIT
