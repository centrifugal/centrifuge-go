This example demonstrates how to use `centrifuge-js` in web browser using WebAssembly.

To run example, compile `main.go` to WASM using:

```bash
GOOS=js GOARCH=wasm go build -o ./assets/example.wasm
```

Then serve assets using any web server, for example with Python:

```bash
cd assets
python3 -m http.server 2000
```

Open web browser on http://localhost:2000, make sure server accepts `http://localhost:2000` Origin, you should observe a WebSocket connection in browser dev tools network tab and output in dev tools JavaScript console.

## WASM file size

By default, `./assets/example.wasm` is quite big in size: 11 MB.

You can strip out debug info and get 10 MB:

```bash
GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o ./assets/example.wasm
```

You can use brotli compression:

```bash
cd assets
brotli example.wasm
❯ ls -lah | grep example.wasm
-rwxr-xr-x  1 alexander.emelin  staff    10M 10 Jun 04:30 example.wasm
-rwxr-xr-x  1 alexander.emelin  staff   1.8M 10 Jun 04:30 example.wasm.br
```

So you get 1.8 MB. But in this case make sure your Web server supports brotli compression and sets `Content-Encoding: br` header for `example.wasm.br` file. See `serve.py` as a very simple example of such server.

We were able to build the example with Tinygo (requires some changes in protocol package, see [tinygo](https://github.com/centrifugal/protocol/compare/tinygo?expand=1) branch), got 5.6 MB size of `example.wasm`, but there was a WASM error in Javascript. So Tinygo does not work for now.

