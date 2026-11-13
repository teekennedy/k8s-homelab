"""Restores Seerr's X-Api-User for clients that still send it.

Seerr let an admin API key act as any user named in an X-Api-User header.
SeerrNG dropped that: its admin key always acts as the owner. The Jellyfin
Enhanced plugin still relies on the header, so through SeerrNG every request
made in Jellyfin would be filed, approved, and quota-checked as the owner.

This sits between the plugin and SeerrNG. Calls without X-Api-User pass
through untouched. Calls with it are rewritten to use SeerrNG's own
"on behalf of" parameters, or refused:

- POST /request gets `userId`, which makes SeerrNG apply that user's quota
  and auto-approval. SeerrNG skips the user's request permission check for
  a request made on their behalf, so it is checked here instead.
- POST /request/<id>/{approve,decline} needs MANAGE_REQUESTS.
- GET /request is filtered to the user's own requests, unless they may view
  everyone's.
- GET /discover/watchlist reads the user's watchlist, not the owner's.
- Any other write is refused rather than made as the owner.
- Other reads are made as the owner. They are not per-user.
"""

from __future__ import annotations

import hmac
import http.client
import json
import os
import re
import sys
import threading
import time
import urllib.parse
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

# server/lib/permissions.ts
ADMIN = 2
MANAGE_REQUESTS = 16
REQUEST = 32
REQUEST_4K = 1024
REQUEST_4K_MOVIE = 2048
REQUEST_4K_TV = 4096
REQUEST_VIEW = 16384
REQUEST_MOVIE = 262144
REQUEST_TV = 524288
REQUEST_MUSIC = 4294967296
REQUEST_BOOK = 34359738368
REQUEST_COMIC = 274877906944
REQUEST_MAGAZINE = 2199023255552
REQUEST_SPORTS = 35184372088832
REQUEST_ADULT = 140737488355328

# hasMediaRequestPermission in server/entity/MediaRequest.ts: any one of these
# allows the request. 4K applies to movies and TV only.
REQUEST_PERMISSIONS = {
    ("movie", False): [REQUEST, REQUEST_MOVIE],
    ("movie", True): [REQUEST_4K, REQUEST_4K_MOVIE],
    ("tv", False): [REQUEST, REQUEST_TV],
    ("tv", True): [REQUEST_4K, REQUEST_4K_TV],
    ("music", False): [REQUEST, REQUEST_MUSIC],
    ("book", False): [REQUEST, REQUEST_BOOK],
    ("comic", False): [REQUEST, REQUEST_COMIC],
    ("magazine", False): [REQUEST, REQUEST_MAGAZINE],
    ("sports", False): [REQUEST, REQUEST_SPORTS],
    ("adult", False): [REQUEST_ADULT],
}

API = "/api/v1"
REQUEST_ACTION = re.compile(rf"^{API}/request/\d+/(approve|decline)/?$")

# Headers that describe one hop, not the message (RFC 9110 section 7.6.1).
HOP_BY_HOP = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
}


def has_any(permissions: int, *wanted: int) -> bool:
    """SeerrNG's hasPermission(..., {type: 'or'}): admins have everything."""
    if permissions & ADMIN:
        return True
    return any(permissions & bit for bit in wanted)


@dataclass(frozen=True)
class Forward:
    """Send the request on, with this path (and query) and body."""

    target: str
    body: bytes


@dataclass(frozen=True)
class Deny:
    status: int
    message: str


def plan(
    method: str, target: str, body: bytes, user_id: int, permissions: int
) -> Forward | Deny:
    """Decides what a request made on behalf of `user_id` becomes."""
    split = urllib.parse.urlsplit(target)
    path = split.path
    query = urllib.parse.parse_qsl(split.query, keep_blank_values=True)

    def with_query(new_path: str, new_query: list[tuple[str, str]]) -> str:
        encoded = urllib.parse.urlencode(new_query)
        return f"{new_path}?{encoded}" if encoded else new_path

    if method in ("GET", "HEAD"):
        if path.rstrip("/") == f"{API}/request" and not has_any(
            permissions, MANAGE_REQUESTS, REQUEST_VIEW
        ):
            query = [(k, v) for k, v in query if k != "requestedBy"]
            query.append(("requestedBy", str(user_id)))
            return Forward(with_query(path, query), body)
        if path.rstrip("/") == f"{API}/discover/watchlist":
            return Forward(with_query(f"{API}/user/{user_id}/watchlist", query), body)
        return Forward(target, body)

    if method == "POST" and path.rstrip("/") == f"{API}/request":
        try:
            request = json.loads(body or b"{}")
        except ValueError:
            return Deny(400, "Request body is not JSON.")
        if not isinstance(request, dict):
            return Deny(400, "Request body is not a JSON object.")
        media_type = request.get("mediaType")
        is_4k = request.get("is4k") is True
        wanted = REQUEST_PERMISSIONS.get((media_type, is_4k))
        if wanted is None:
            return Deny(400, f"Unsupported request: {media_type!r}, 4K={is_4k}.")
        if not has_any(permissions, *wanted):
            return Deny(403, "You do not have permission to make this request.")
        request["userId"] = user_id
        return Forward(target, json.dumps(request).encode())

    if method == "POST" and REQUEST_ACTION.match(path):
        if not has_any(permissions, MANAGE_REQUESTS):
            return Deny(403, "You do not have permission to manage requests.")
        return Forward(target, body)

    return Deny(403, "This proxy only makes requests on a user's behalf.")


class UserCache:
    """Seerr users' permissions, briefly cached: a page load makes many calls."""

    def __init__(self, fetch, ttl: float):
        self._fetch = fetch
        self._ttl = ttl
        self._lock = threading.Lock()
        self._entries: dict[int, tuple[float, int | None]] = {}

    def permissions(self, user_id: int) -> int | None:
        now = time.monotonic()
        with self._lock:
            entry = self._entries.get(user_id)
            if entry and now - entry[0] < self._ttl:
                return entry[1]
        value = self._fetch(user_id)
        with self._lock:
            self._entries[user_id] = (now, value)
        return value


@dataclass(frozen=True)
class Config:
    seerr: urllib.parse.SplitResult
    api_key: str
    port: int
    user_cache_seconds: float

    @classmethod
    def from_env(cls, env=os.environ) -> Config:
        seerr = urllib.parse.urlsplit(env["SEERR_URL"])
        if seerr.scheme != "http" or not seerr.hostname:
            raise ValueError("SEERR_URL must be an http:// URL")
        return cls(
            seerr=seerr,
            api_key=env["SEERR_API_KEY"],
            port=int(env.get("PORT", "8080")),
            user_cache_seconds=float(env.get("USER_CACHE_SECONDS", "60")),
        )


def upstream(config: Config) -> http.client.HTTPConnection:
    return http.client.HTTPConnection(
        config.seerr.hostname, config.seerr.port or 80, timeout=60
    )


def fetch_permissions(config: Config, user_id: int) -> int | None:
    """The user's permission bits, or None if Seerr has no such user."""
    connection = upstream(config)
    try:
        connection.request(
            "GET",
            f"{API}/user/{user_id}",
            headers={"X-Api-Key": config.api_key, "Accept": "application/json"},
        )
        response = connection.getresponse()
        payload = response.read()
    finally:
        connection.close()
    if response.status == 404:
        return None
    if response.status != 200:
        raise RuntimeError(f"GET /user/{user_id} returned {response.status}")
    return int(json.loads(payload)["permissions"])


def make_handler(config: Config, users: UserCache):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self):
            if self.path == "/healthz":
                self._reply(200, b"ok\n", "text/plain")
                return
            self._handle()

        def _handle(self):
            length = int(self.headers.get("Content-Length") or 0)
            body = self.rfile.read(length) if length else b""
            target = self.path
            headers = {
                k: v
                for k, v in self.headers.items()
                if k.lower() not in HOP_BY_HOP
                and k.lower() not in ("host", "content-length", "x-api-user")
            }

            api_user = self.headers.get("X-Api-User")
            if api_user is not None:
                # Only the admin key may name a user, as in Seerr. Anything else
                # is passed on without the header for Seerr to reject or serve
                # as whoever the key belongs to.
                if hmac.compare_digest(
                    self.headers.get("X-Api-Key", "").encode(),
                    config.api_key.encode(),
                ):
                    try:
                        user_id = int(api_user)
                    except ValueError:
                        self._deny(Deny(400, "X-Api-User is not a user ID."))
                        return
                    try:
                        permissions = users.permissions(user_id)
                    except Exception as error:  # noqa: BLE001 -- any upstream failure
                        self.log_error("looking up user %d: %s", user_id, error)
                        self._deny(Deny(502, "Could not look up the Seerr user."))
                        return
                    if permissions is None:
                        self._deny(Deny(403, f"No Seerr user {user_id}."))
                        return
                    decision = plan(self.command, target, body, user_id, permissions)
                    if isinstance(decision, Deny):
                        self.log_message(
                            "denied %s %s for user %d: %s",
                            self.command,
                            target,
                            user_id,
                            decision.message,
                        )
                        self._deny(decision)
                        return
                    target, body = decision.target, decision.body

            self._forward(target, headers, body)

        do_HEAD = do_POST = do_PUT = do_PATCH = do_DELETE = _handle

        def _forward(self, target: str, headers: dict[str, str], body: bytes):
            connection = upstream(config)
            try:
                connection.request(self.command, target, body=body, headers=headers)
                response = connection.getresponse()
                payload = response.read()
            except OSError as error:
                self.log_error("forwarding %s %s: %s", self.command, target, error)
                self._deny(Deny(502, "Seerr is unreachable."))
                return
            finally:
                connection.close()
            self.send_response(response.status, response.reason)
            for key, value in response.getheaders():
                if key.lower() not in HOP_BY_HOP and key.lower() != "content-length":
                    self.send_header(key, value)
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(payload)

        def _deny(self, decision: Deny):
            body = json.dumps({"message": decision.message}).encode()
            self._reply(decision.status, body, "application/json")

        def _reply(self, status: int, body: bytes, content_type: str):
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)

    return Handler


class Server(ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        # A client closing a kept-alive connection is routine, not an error.
        if isinstance(sys.exc_info()[1], (ConnectionResetError, BrokenPipeError)):
            return
        super().handle_error(request, client_address)


def serve(config: Config) -> ThreadingHTTPServer:
    users = UserCache(
        lambda user_id: fetch_permissions(config, user_id), config.user_cache_seconds
    )
    return Server(("", config.port), make_handler(config, users))


def main() -> int:
    config = Config.from_env()
    server = serve(config)
    print(f"listening on :{config.port}, forwarding to {config.seerr.geturl()}")
    server.serve_forever()
    return 0


if __name__ == "__main__":
    sys.exit(main())
