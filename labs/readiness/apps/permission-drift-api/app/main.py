import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/orders/readiness":
            self.write_json(404, {"error": "not found"})
            return

        try:
            Path("storage/logs/audit.log").write_text("readiness audit\n")
        except OSError as error:
            self.write_json(500, {"detail": f"permission drift: {error}"})
            return

        self.write_json(200, {"status": "FIXED", "lane": "permission-drift"})

    def write_json(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
