import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

API_KEY = "test-api-key"
CONFIG_PATH = "/api/v3/config/host"


class StubArr:
    """Serves an arr app's host config at CONFIG_PATH and records requests."""

    def __init__(self):
        self.config = {
            "id": 1,
            "bindAddress": "*",
            "port": 7878,
            "username": "admin",
            "password": "stored-hash",
            "passwordConfirmation": "",
            "backupFolder": "Backups",
            "backupInterval": 7,
            "backupRetention": 28,
        }
        self.requests: list[dict] = []


@pytest.fixture
def arr():
    stub = StubArr()

    class Handler(BaseHTTPRequestHandler):
        def _record(self, body=None):
            stub.requests.append(
                {
                    "method": self.command,
                    "path": self.path,
                    "headers": dict(self.headers),
                    "body": body,
                }
            )

        def _reply(self, obj):
            payload = json.dumps(obj).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def do_GET(self):
            self._record()
            if self.path != CONFIG_PATH:
                self.send_error(404)
                return
            self._reply(stub.config)

        def do_PUT(self):
            length = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(length))
            self._record(body)
            if self.path != f"{CONFIG_PATH}/{stub.config['id']}":
                self.send_error(404)
                return
            stub.config = {**body, "password": stub.config["password"]}
            self._reply(stub.config)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    stub.url = f"http://127.0.0.1:{server.server_port}{CONFIG_PATH}"
    yield stub
    server.shutdown()
    server.server_close()
