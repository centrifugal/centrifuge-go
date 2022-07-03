v0.8.3
======

* Call OnServerSubscribe handler correctly for dynamic server subscriptions (happening after initial connect), see [#64](https://github.com/centrifugal/centrifuge-go/pull/64).

v0.8.2
======

* Update protocol to v0.7.3

v0.8.1
======

* Support for History reverse option.

```
gorelease -base v0.8.0 -version v0.8.1
github.com/centrifugal/centrifuge-go
------------------------------------
Compatible changes:
- HistoryOptions.Reverse: added
- WithHistoryReverse: added

v0.8.1 is a valid semantic version for this release.
```

v0.8.0
======

Update to work with Centrifuge >= v0.18.0 and Centrifugo v3.

Keep in mind that `New` is deprecated now, prefer using `NewJsonClient` or `NewProtobufClient` when server is based on Centrifuge >= v0.18.0 or Centrifugo >= v3.0.0

**Breaking change:** client History API behavior changed – Centrifuge >= v0.18.0 and Centrifugo >= v3.0.0 won't return all publications in a stream by default, see Centrifuge [v0.18.0 release notes](https://github.com/centrifugal/centrifuge/releases/tag/v0.18.0) or [Centrifugo v3 migration guide](https://centrifugal.dev/docs/getting-started/migration_v3) for more information and workaround on server-side.

```
gorelease -base v0.7.2 -version v0.8.0
github.com/centrifugal/centrifuge-go
------------------------------------
Incompatible changes:
- (*Client).History: changed from func(string) (HistoryResult, error) to func(string, ...HistoryOption) (HistoryResult, error)
- (*Subscription).History: changed from func() (HistoryResult, error) to func(...HistoryOption) (HistoryResult, error)
- (*Subscription).Subscribe: changed from func() error to func(...SubscribeOption) error
Compatible changes:
- HistoryOption: added
- HistoryOptions: added
- NewJsonClient: added
- NewProtobufClient: added
- StreamPosition: added
- SubscribeOption: added
- SubscribeOptions: added
- WithHistoryLimit: added
- WithHistorySince: added
- WithSubscribeSince: added

v0.8.0 is a valid semantic version for this release.
```

v0.7.2
======

* Bump protocol to v0.5.0 [#56](https://github.com/centrifugal/centrifuge-go/pull/56)

v0.7.1
======

* Fix atomic align on 32-bit [#49](https://github.com/centrifugal/centrifuge-go/pull/49)

v0.7.0
======

* Updated `github.com/centrifugal/protocol` package dependency to catch up with the latest changes in it
* Introduced `Error` type which is used where we previously exposed `protocol.Error` – so there is no need to import `protocol` package in application code to investigate error code or message
* Methods `Client.SetName` and `Client.SetVersion` removed in favour of `Name` and `Version` fields of `Config`
* Add top-level methods `Client.History`, `Client.Presence`, `Client.PresenceStats` – so it's possible to call corresponding client API methods when using server-side subscriptions

```
$ gorelease -base v0.6.5 -version v0.7.0
github.com/centrifugal/centrifuge-go
------------------------------------
Incompatible changes:
- (*Client).SetName: removed
- (*Client).SetVersion: removed
Compatible changes:
- (*Client).History: added
- (*Client).Presence: added
- (*Client).PresenceStats: added
- Config.Name: added
- Config.Version: added
- DefaultName: added
- Error: added

v0.7.0 is a valid semantic version for this release.
```

v0.6.5
======

* One more fix for memory align on 32bit arch, see [#46](https://github.com/centrifugal/centrifuge-go/pull/46)

v0.6.4
======

* Add `Subscription.Close` method to close Subscription when it's not needed anymore. This method unsubscribes from a channel and removes Subscription from internal `Client` subscription registry – thus freeing resources. Subscription is not usable after `Close` called. This method can be helpful if you work with lots of short-living subscriptions to different channels to prevent unlimited internal Subscription registry growth.

v0.6.3
======

* Fix memory align on 32bit arch, see [#40](https://github.com/centrifugal/centrifuge-go/pull/40)

v0.6.2
======

* fix deadlock on a private channel resubscribe - see [#38](https://github.com/centrifugal/centrifuge-go/pull/38)

v0.6.1
======

* fix setting server-side unsubscribe handler, call server-side unsubscribe event on disconnect 

v0.6.0
======

* server-side subscriptions support
* get rid of Protobuf protocol struct `Publication` and `ClientInfo` aliases – use library scope structures instead
* change return values of `RPC`, `NamedRPC`, `History`, `Presence`, `PresenceStats`, `Publish` methods to be more meaningful and extensible
* much faster resubscribe to many subscriptions (previously we waited for each individual subscription response before moving further, now process is asynchronous)
* improved reconnect logic
* Client and Subscription status refactoring
* fix inconsistent join/subscribe event ordering – now both processed in order coming from server

v0.5.2
======

* `NamedRPC` method added - [#35](https://github.com/centrifugal/centrifuge-go/pull/35), thanks [@L11R](https://github.com/L11R)
