"""The proxy end to end, in front of a fake SeerrNG."""

from __future__ import annotations

import http.client
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

import proxy

API_KEY = "admin-key"
PERMISSIONS = {7: proxy.REQUEST, 8: proxy.MANAGE_REQUESTS}


class FakeSeerr(BaseHTTPRequestHandler):
    """Echoes what reached it, and serves GET /api/v1/user/<id>."""

    protocol_version = "HTTP/1.1"
    received: list[dict] = []

    def _handle(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode() if length else ""
        if self.command == "GET" and self.path.startswith("/api/v1/user/"):
            user_id = int(self.path.rsplit("/", 1)[1])
            if user_id not in PERMISSIONS:
                self._reply(404, {"message": "not found"})
                return
            self._reply(200, {"id": user_id, "permissions": PERMISSIONS[user_id]})
            return
        seen = {
            "method": self.command,
            "path": self.path,
            "headers": {k.lower(): v for k, v in self.headers.items()},
            "body": body,
        }
        FakeSeerr.received.append(seen)
        self._reply(201 if self.command == "POST" else 200, seen)

    do_GET = do_POST = do_DELETE = _handle

    def _reply(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def run(server: ThreadingHTTPServer):
    thread = threading.Thread(
        target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
    )
    thread.start()
    return thread


@pytest.fixture
def proxy_port():
    FakeSeerr.received = []
    seerr = ThreadingHTTPServer(("127.0.0.1", 0), FakeSeerr)
    run(seerr)
    config = proxy.Config.from_env(
        {
            "SEERR_URL": f"http://127.0.0.1:{seerr.server_address[1]}",
            "SEERR_API_KEY": API_KEY,
            "PORT": "0",
        }
    )
    server = proxy.serve(config)
    run(server)
    yield server.server_address[1]
    server.shutdown()
    seerr.shutdown()


def call(port, method, path, headers=None, body=None):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        connection.request(
            method,
            path,
            body=json.dumps(body) if body is not None else None,
            headers={"Content-Type": "application/json", **(headers or {})},
        )
        response = connection.getresponse()
        return response.status, json.loads(response.read() or b"null")
    finally:
        connection.close()


def as_user(user_id, key=API_KEY):
    return {"X-Api-Key": key, "X-Api-User": str(user_id)}


def test_healthz(proxy_port):
    connection = http.client.HTTPConnection("127.0.0.1", proxy_port, timeout=5)
    try:
        connection.request("GET", "/healthz")
        assert connection.getresponse().status == 200
    finally:
        connection.close()


def test_calls_without_x_api_user_pass_through_untouched(proxy_port):
    status, seen = call(
        proxy_port,
        "POST",
        "/api/v1/request",
        {"X-Api-Key": API_KEY},
        {"mediaType": "movie", "mediaId": 1},
    )
    assert status == 201
    assert json.loads(seen["body"]) == {"mediaType": "movie", "mediaId": 1}
    assert seen["headers"]["x-api-key"] == API_KEY


def test_request_reaches_seerr_on_the_users_behalf(proxy_port):
    status, seen = call(
        proxy_port,
        "POST",
        "/api/v1/request",
        as_user(7),
        {"mediaType": "movie", "mediaId": 1},
    )
    assert status == 201
    assert json.loads(seen["body"])["userId"] == 7
    assert "x-api-user" not in seen["headers"]
    assert seen["headers"]["x-api-key"] == API_KEY


def test_refusals_never_reach_seerr(proxy_port):
    status, body = call(proxy_port, "POST", "/api/v1/issue", as_user(7), {})
    assert status == 403
    assert "message" in body
    status, _ = call(proxy_port, "POST", "/api/v1/request/1/approve", as_user(7), {})
    assert status == 403
    assert FakeSeerr.received == []


def test_managers_may_approve(proxy_port):
    status, seen = call(proxy_port, "POST", "/api/v1/request/1/approve", as_user(8), {})
    assert status == 201
    assert seen["path"] == "/api/v1/request/1/approve"


def test_unknown_users_are_refused(proxy_port):
    status, _ = call(
        proxy_port, "POST", "/api/v1/request", as_user(99), {"mediaType": "movie"}
    )
    assert status == 403
    assert FakeSeerr.received == []


def test_reads_are_rewritten_for_the_user(proxy_port):
    status, seen = call(proxy_port, "GET", "/api/v1/request?take=5", as_user(7))
    assert status == 200
    assert seen["path"] == "/api/v1/request?take=5&requestedBy=7"


def test_a_wrong_key_cannot_name_a_user(proxy_port):
    # The header is dropped and the call goes on as-is, for Seerr to reject.
    status, seen = call(
        proxy_port,
        "POST",
        "/api/v1/request",
        as_user(7, key="wrong"),
        {"mediaType": "tv", "mediaId": 1},
    )
    assert status == 201
    assert "userId" not in json.loads(seen["body"])
    assert "x-api-user" not in seen["headers"]


def test_bad_user_ids_are_refused(proxy_port):
    status, _ = call(proxy_port, "GET", "/api/v1/request", as_user("seven"))
    assert status == 400
