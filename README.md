[![GoDoc](https://pkg.go.dev/badge/centrifugal/centrifuge-go)](https://pkg.go.dev/github.com/centrifugal/centrifuge-go)

Websocket client for [Centrifugo](https://github.com/centrifugal/centrifugo) server and [Centrifuge](https://github.com/centrifugal/centrifuge) library.

There is no v1 release of this library yet – API still evolves. At the moment patch version updates only contain backwards compatible changes, minor version updates can have backwards incompatible API changes.

Check out [client SDK API specification](https://centrifugal.dev/docs/transports/client_api) to learn how this SDK behaves. It's recommended to read that before starting to work with this SDK as the spec covers common SDK behavior - describes client and subscription state transitions, main options and methods. Also check out examples folder.

The features implemented by this SDK can be found in [SDK feature matrix](https://centrifugal.dev/docs/transports/client_sdk#sdk-feature-matrix).

## Run tests

First run Centrifugo instance:

```
docker run -d -p 8000:8000 centrifugo/centrifugo:latest centrifugo --client_insecure
```

Then run `go test`
