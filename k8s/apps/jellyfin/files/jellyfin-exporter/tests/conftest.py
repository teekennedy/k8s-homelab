"""A stub Jellyfin server, and the switch between it and a real one.

The exporter talks to Jellyfin two ways: `GET /Sessions` over HTTP and a
`/socket` WebSocket. `FakeJellyfin` serves both on one port, close enough
to the real thing that the integration tests can run against it
unattended in CI -- which is the point, because tests that only run when
somebody remembers to port-forward do not run.

Set JELLYFIN_URL and JELLYFIN_API_KEY to point the same tests at a live
server instead; see test_integration.py.
"""

from __future__ import annotations

import json
import os
import threading
import urllib.parse

import pytest
from websockets.datastructures import Headers
from websockets.http11 import Response
from websockets.sync.server import ServerConnection, serve

STUB_API_KEY = "stub-api-key"


def transcoding_session(user: str = "alice") -> dict:
    """A session doing real video work, with the fields the exporter reads."""
    return {
        "Id": "session-1",
        "UserName": user,
        "Client": "Jellyfin Web",
        "DeviceName": "Living Room TV",
        "NowPlayingItem": {"Id": "item-1", "Name": "Something"},
        "PlayState": {"IsPaused": False, "PlayMethod": "Transcode"},
        "TranscodingInfo": {
            "IsVideoDirect": False,
            "IsAudioDirect": False,
            "HardwareAccelerationType": "vaapi",
            "TranscodeReasons": ["VideoCodecNotSupported"],
        },
    }


def _json_response(status: int, reason: str, payload) -> Response:
    body = json.dumps(payload).encode()
    return Response(
        status,
        reason,
        Headers(
            {
                "Content-Type": "application/json",
                "Content-Length": str(len(body)),
            }
        ),
        body,
    )


class FakeJellyfin:
    """Speaks enough of Jellyfin to drive the exporter end to end.

    Serves `GET /Sessions` and the `/socket` WebSocket on one port, and
    rejects both when the API key is wrong -- so the tests that assert a
    bad key is refused are testing something.
    """

    def __init__(self, sessions=None, api_key: str = STUB_API_KEY, rest_status=200):
        self.api_key = api_key
        self.sessions = (
            list(sessions) if sessions is not None else [transcoding_session()]
        )
        # Lets a test force a REST failure without stopping the server.
        self.rest_status = rest_status
        # (path, headers); headers stay a Headers so lookups are
        # case-insensitive, as they are on the wire.
        self.rest_requests: list[tuple[str, Headers]] = []
        self.handshake_paths: list[str] = []
        self.rejected_handshakes: list[str] = []
        self.client_messages: list[dict] = []
        # Sent after the client subscribes. None means "the current sessions".
        self.to_send: list[dict] | None = None

        self._lock = threading.Lock()
        self._connections: list[ServerConnection] = []
        self._server = serve(
            self._handle,
            "127.0.0.1",
            0,
            process_request=self._process_request,
        )
        self.port = self._server.socket.getsockname()[1]
        self.url = f"http://127.0.0.1:{self.port}"
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)

    def __enter__(self) -> FakeJellyfin:
        self._thread.start()
        return self

    def __exit__(self, *_) -> None:
        self._server.shutdown()

    # -- server side ------------------------------------------------------

    def _process_request(
        self, connection: ServerConnection, request
    ) -> Response | None:
        path, _, query = request.path.partition("?")

        if path == "/socket":
            # Jellyfin authenticates the socket by query parameter; there is
            # no header to carry the token through a browser upgrade.
            params = urllib.parse.parse_qs(query)
            if params.get("api_key", [""])[0] != self.api_key:
                with self._lock:
                    self.rejected_handshakes.append(request.path)
                return _json_response(401, "Unauthorized", {"error": "bad api key"})
            with self._lock:
                self.handshake_paths.append(request.path)
            return None

        # Anything else is a plain HTTP request; answer it and never upgrade.
        with self._lock:
            self.rest_requests.append((request.path, request.headers))

        if request.headers.get("X-Emby-Token") != self.api_key:
            return _json_response(401, "Unauthorized", {"error": "bad api key"})
        if path.rstrip("/") != "/Sessions":
            return _json_response(404, "Not Found", {"error": "no such endpoint"})
        if self.rest_status != 200:
            return _json_response(self.rest_status, "Error", {"error": "forced"})
        with self._lock:
            return _json_response(200, "OK", list(self.sessions))

    def _handle(self, connection: ServerConnection) -> None:
        with self._lock:
            self._connections.append(connection)
        try:
            for raw in connection:
                message = json.loads(raw)
                with self._lock:
                    self.client_messages.append(message)
                if message.get("MessageType") != "SessionsStart":
                    continue
                for payload in self._initial_messages():
                    connection.send(json.dumps(payload))
        except Exception:  # noqa: BLE001 - the client hanging up is normal
            return
        finally:
            with self._lock:
                if connection in self._connections:
                    self._connections.remove(connection)

    def _initial_messages(self) -> list[dict]:
        with self._lock:
            if self.to_send is not None:
                return list(self.to_send)
            return [{"MessageType": "Sessions", "Data": list(self.sessions)}]

    # -- test helpers -----------------------------------------------------

    def push(self, message: dict) -> None:
        """Send a message to every connected client."""
        encoded = json.dumps(message)
        with self._lock:
            connections = list(self._connections)
        for connection in connections:
            connection.send(encoded)

    def disconnect_all(self) -> None:
        """Hang up on every connected client, as a restarting Jellyfin would."""
        with self._lock:
            connections = list(self._connections)
        for connection in connections:
            connection.close()

    def sent_message_types(self) -> list[str]:
        with self._lock:
            return [m.get("MessageType") for m in self.client_messages]


@pytest.fixture
def stub_jellyfin():
    """A stub server, fresh per test."""
    with FakeJellyfin() as server:
        yield server


@pytest.fixture(scope="session")
def jellyfin_target():
    """(base_url, api_key) for the integration tests.

    A live server when JELLYFIN_URL and JELLYFIN_API_KEY are both set,
    otherwise the stub. Same tests either way.
    """
    url = os.environ.get("JELLYFIN_URL", "").strip()
    api_key = os.environ.get("JELLYFIN_API_KEY", "").strip()
    if url and api_key:
        yield url, api_key
        return
    with FakeJellyfin() as server:
        yield server.url, server.api_key


@pytest.fixture(scope="session")
def jellyfin_is_live() -> bool:
    return bool(
        os.environ.get("JELLYFIN_URL", "").strip()
        and os.environ.get("JELLYFIN_API_KEY", "").strip()
    )
