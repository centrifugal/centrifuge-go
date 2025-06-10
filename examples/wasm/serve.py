from http.server import SimpleHTTPRequestHandler, HTTPServer
import os

ASSETS_DIR = "assets"
PORT = 2000

class BrotliWasmHandler(SimpleHTTPRequestHandler):
    def translate_path(self, path):
        # Serve files from the assets directory
        return os.path.join(os.getcwd(), ASSETS_DIR, path.lstrip("/"))

    def end_headers(self):
        # Serve correct headers for .wasm.br
        if self.path.endswith(".wasm.br"):
            self.send_header("Content-Encoding", "br")
        # Don't override headers for .wasm – the base handler sets them properly
        super().end_headers()

if __name__ == "__main__":
    os.chdir(".")
    httpd = HTTPServer(("localhost", PORT), BrotliWasmHandler)
    print(f"Serving '{ASSETS_DIR}/' on http://localhost:{PORT}")
    httpd.serve_forever()
