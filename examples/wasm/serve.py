from http.server import SimpleHTTPRequestHandler, HTTPServer
import os

ASSETS_DIR = "assets"
PORT = 2000

class WasmHandler(SimpleHTTPRequestHandler):
    def translate_path(self, path):
        # Serve files from the assets directory
        return os.path.join(os.getcwd(), ASSETS_DIR, path.lstrip("/"))

    def send_head(self):
        path = self.translate_path(self.path)
        encoding = None

        # Set encoding and proper content-type for compressed .wasm
        if path.endswith(".wasm.br") and os.path.exists(path):
            encoding = "br"
            content_type = "application/wasm"
        elif path.endswith(".wasm.gz") and os.path.exists(path):
            encoding = "gzip"
            content_type = "application/wasm"
        else:
            return super().send_head()  # fallback to default handler

        # Serve compressed wasm manually
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Encoding", encoding)
        fs = os.stat(path)
        self.send_header("Content-Length", str(fs.st_size))
        self.end_headers()
        return open(path, 'rb')

if __name__ == "__main__":
    os.chdir(".")
    httpd = HTTPServer(("localhost", PORT), WasmHandler)
    print(f"Serving '{ASSETS_DIR}/' on http://localhost:{PORT}")
    httpd.serve_forever()
