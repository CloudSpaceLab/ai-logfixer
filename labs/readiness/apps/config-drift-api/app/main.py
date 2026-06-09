import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from urllib.error import URLError
from urllib.request import urlopen


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/orders/readiness":
            self.write_json(404, {"error": "not found"})
            return

        config = json.loads(Path("config/runtime.json").read_text())
        upstream = config["api_base_url"].rstrip("/") + "/health"
        try:
            with urlopen(upstream, timeout=2) as response:
                if response.status != 200:
                    self.write_json(502, {"detail": f"upstream returned {response.status}"})
                    return
        except (OSError, URLError) as error:
            self.write_json(502, {"detail": f"bad config api_base_url={config['api_base_url']}: {error}"})
            return

        self.write_json(200, {"status": "FIXED", "lane": "config-drift"})

    def write_json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
