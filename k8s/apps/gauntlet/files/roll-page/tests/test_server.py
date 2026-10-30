"""Runs the roll page server against a stub RomM API."""

from __future__ import annotations

import json
import os
import threading
import urllib.error
import urllib.request
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

import server

TOKEN = "rmm_test"

COLLECTIONS = [
    {"id": 1, "name": "Favourites"},
    {"id": 7, "name": "Gauntlet"},
]

ROMS = {
    7: [
        {
            "id": 11,
            "name": "Sonic the Hedgehog 2",
            "fs_name_no_ext": "Sonic 2",
            "platform_display_name": "Genesis",
            "missing_from_fs": False,
        },
        {
            "id": 12,
            "name": None,
            "fs_name_no_ext": "Bomberman",
            "platform_display_name": "SNES",
            "missing_from_fs": False,
        },
        {
            "id": 13,
            "name": "Deleted Game",
            "platform_display_name": "NES",
            "missing_from_fs": True,
        },
    ],
}


class StubRomm(BaseHTTPRequestHandler):
    requests: list[tuple[str, str | None]] = []

    def log_message(self, *args: object) -> None:
        pass

    def do_GET(self) -> None:
        StubRomm.requests.append((self.path, self.headers.get("Authorization")))
        if self.headers.get("Authorization") != f"Bearer {TOKEN}":
            self.send_response(401)
            self.end_headers()
            return
        path, _, query = self.path.partition("?")
        if path == "/api/collections":
            body = COLLECTIONS
        elif path == "/api/roms":
            params = dict(p.split("=") for p in query.split("&"))
            items = ROMS.get(int(params["collection_id"]), [])
            body = {"items": items, "total": len(items)}
        else:
            self.send_response(404)
            self.end_headers()
            return
        data = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def _start(httpd: ThreadingHTTPServer) -> str:
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{httpd.server_address[1]}"


@pytest.fixture
def romm_url() -> Iterator[str]:
    StubRomm.requests = []
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), StubRomm)
    yield _start(httpd)
    httpd.shutdown()


@pytest.fixture(autouse=True)
def static_dir(tmp_path, monkeypatch) -> None:
    # CI only uploads Python sources, so the real static/ isn't available.
    (tmp_path / "index.html").write_text("<title>The Gauntlet</title>")
    (tmp_path / "app.js").write_text("wheel.spinToItem(0);")
    monkeypatch.setenv("TEST_STATIC_DIR", str(tmp_path))


def make_app(romm_url: str, **overrides: str) -> Iterator[str]:
    env = {
        "STATIC_DIR": os.environ["TEST_STATIC_DIR"],
        "ROMM_URL": romm_url,
        "ROMM_PUBLIC_URL": "https://romm.example",
        "ROMM_API_TOKEN": TOKEN,
        "ROMM_COLLECTION": "gauntlet",
        "PORT": "0",
        **overrides,
    }
    httpd = server.serve(server.Config.from_env(env))
    yield _start(httpd)
    httpd.shutdown()


@pytest.fixture
def app(romm_url: str) -> Iterator[str]:
    yield from make_app(romm_url)


def call(url: str, body: object | None = None) -> tuple[int, object]:
    data = None if body is None else json.dumps(body).encode()
    request = urllib.request.Request(url, data=data, method="POST" if data else "GET")
    try:
        with urllib.request.urlopen(request) as response:
            raw = response.read()
            status = response.status
    except urllib.error.HTTPError as e:
        raw = e.read()
        status = e.code
    try:
        return status, json.loads(raw)
    except ValueError:
        return status, raw.decode()


def test_pool_lists_collection_games(app: str) -> None:
    status, body = call(f"{app}/api/pool")

    assert status == 200
    assert body["games"] == [
        {
            "id": 12,
            "name": "Bomberman",
            "platform": "SNES",
            "playUrl": "https://romm.example/rom/12/ejs",
        },
        {
            "id": 11,
            "name": "Sonic the Hedgehog 2",
            "platform": "Genesis",
            "playUrl": "https://romm.example/rom/11/ejs",
        },
    ]


def test_pool_sends_token(app: str) -> None:
    call(f"{app}/api/pool")

    assert StubRomm.requests
    assert all(auth == f"Bearer {TOKEN}" for _, auth in StubRomm.requests)
    assert any("collection_id=7" in path for path, _ in StubRomm.requests)


def test_roll_picks_from_pool(app: str) -> None:
    status, body = call(f"{app}/api/roll", {})

    assert status == 200
    assert body["game"]["id"] in {11, 12}
    assert len(body["games"]) == 2


def test_roll_skips_excluded(app: str) -> None:
    for _ in range(20):
        status, body = call(f"{app}/api/roll", {"exclude": [11]})
        assert status == 200
        assert body["game"]["id"] == 12


def test_roll_falls_back_when_everything_is_excluded(app: str) -> None:
    status, body = call(f"{app}/api/roll", {"exclude": [11, 12]})

    assert status == 200
    assert body["game"]["id"] in {11, 12}


def test_roll_rejects_bad_body(app: str) -> None:
    status, _ = call(f"{app}/api/roll", {"exclude": ["x"]})

    assert status == 400


def test_missing_collection(romm_url: str) -> None:
    app = make_app(romm_url, ROMM_COLLECTION="Nope")
    url = next(app)
    status, body = call(f"{url}/api/pool")

    assert status == 502
    assert "Nope" in body["error"]


def test_bad_token(romm_url: str) -> None:
    app = make_app(romm_url, ROMM_API_TOKEN="rmm_wrong")
    url = next(app)
    status, body = call(f"{url}/api/roll", {})

    assert status == 502
    assert "token" in body["error"]


def test_unreachable_romm() -> None:
    app = make_app("http://127.0.0.1:9")
    url = next(app)
    status, body = call(f"{url}/api/pool")

    assert status == 502
    assert "reach" in body["error"]


def test_custom_play_path(romm_url: str) -> None:
    app = make_app(romm_url, ROMM_PLAY_PATH="/console/rom/{id}/play")
    url = next(app)
    _, body = call(f"{url}/api/pool")

    assert body["games"][0]["playUrl"] == "https://romm.example/console/rom/12/play"


def test_serves_page_and_static_files(app: str) -> None:
    status, body = call(f"{app}/")
    assert status == 200
    assert "<title>The Gauntlet</title>" in body

    status, body = call(f"{app}/static/app.js")
    assert status == 200
    assert "spinToItem" in body


@pytest.mark.parametrize(
    "path",
    ["/static/../server.py", "/static/%2e%2e/server.py", "/server.py", "/app.js"],
)
def test_does_not_serve_other_files(app: str, path: str) -> None:
    status, _ = call(f"{app}{path}")

    assert status == 404


def test_healthz(app: str) -> None:
    status, body = call(f"{app}/healthz")

    assert status == 200
    assert body == "ok\n"
