"""Serve exact Go-produced candidate control responses to the owned clients."""
import http.server
import base64
import json
import sys
import urllib.parse

with open(sys.argv[1], "rb") as fixture_file:
    fixtures = json.load(fixture_file)


def key(path):
    parsed = urllib.parse.urlsplit(path)
    return parsed.path, tuple(sorted(urllib.parse.parse_qsl(parsed.query)))


responses = {key(path): base64.b64decode(body, validate=True)
             for path, body in fixtures.items()}


class Handler(http.server.BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(204)
        self.cors()
        self.end_headers()

    def cors(self):
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Headers", "Authorization, Content-Type")
        self.send_header("Access-Control-Allow-Methods", "GET, OPTIONS")

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer " + "R" * 43:
            self.send_error(403)
            return
        body = responses.get(key(self.path))
        if body is None:
            self.send_error(404)
            return
        self.send_response(200)
        self.cors()
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
print("http://127.0.0.1:" + str(server.server_port), flush=True)
server.serve_forever()
