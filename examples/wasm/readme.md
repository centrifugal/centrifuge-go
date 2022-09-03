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
