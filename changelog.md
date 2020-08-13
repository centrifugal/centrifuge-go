v0.6.0
======

* server-side subscriptions support
* get rid of Protobuf protocol structure aliases
* change return values of `RPC`, `NamedRPC`, `History`, `Presence`, `PresenceStats`, `Publish` methods to be more meaningful and extensible
* improved reconnect logic
* Client and Subscription status refactoring
* fix inconsistent join/subscribe event ordering – now both processed in order coming from server

v0.5.2
======

* `NamedRPC` method added - [#35](https://github.com/centrifugal/centrifuge-go/pull/35), thanks [@L11R](https://github.com/L11R)
