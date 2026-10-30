"""Roll page server for The Gauntlet.

Serves the TV page and picks a random game from a RomM collection. The pick
happens here, before the page spins its wheel; the wheel only animates
towards it. Nothing is stored: results live in Bracket.

Standard library only, so the pod runs it with a bare interpreter.
"""

from __future__ import annotations

import json
import logging
import mimetypes
import os
import secrets
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

log = logging.getLogger("roll-page")

# Upper bound for one page of /api/roms; a hand-picked collection is far
# smaller.
MAX_GAMES = 10000


class RommError(Exception):
    """RomM could not supply the game pool. The message is shown on the TV."""


@dataclass(frozen=True)
class Config:
    romm_url: str
    romm_public_url: str
    romm_token: str
    collection: str
    play_path: str
    static_dir: Path
    port: int

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> Config:
        env = os.environ if env is None else env
        return cls(
            romm_url=env.get("ROMM_URL", "http://romm:8080").rstrip("/"),
            romm_public_url=env.get("ROMM_PUBLIC_URL", "").rstrip("/"),
            romm_token=env.get("ROMM_API_TOKEN", ""),
            collection=env.get("ROMM_COLLECTION", "Gauntlet"),
            play_path=env.get("ROMM_PLAY_PATH", "/rom/{id}/ejs"),
            static_dir=Path(
                env.get("STATIC_DIR", Path(__file__).resolve().parent / "static")
            ),
            port=int(env.get("PORT", "8080")),
        )


class Romm:
    def __init__(self, config: Config) -> None:
        self.config = config

    def _get(self, path: str, params: dict[str, object] | None = None) -> object:
        if not self.config.romm_token:
            raise RommError("No RomM API token is configured.")
        url = self.config.romm_url + path
        if params:
            url += "?" + urllib.parse.urlencode(params)
        request = urllib.request.Request(
            url,
            headers={
                "Authorization": f"Bearer {self.config.romm_token}",
                "Accept": "application/json",
            },
        )
        try:
            with urllib.request.urlopen(request, timeout=10) as response:
                return json.load(response)
        except urllib.error.HTTPError as e:
            if e.code in (HTTPStatus.UNAUTHORIZED, HTTPStatus.FORBIDDEN):
                raise RommError(
                    "RomM rejected the API token. It needs the roms.read and "
                    "collections.read scopes."
                ) from e
            raise RommError(f"RomM answered {path} with HTTP {e.code}.") from e
        except (urllib.error.URLError, TimeoutError) as e:
            raise RommError(f"Could not reach RomM at {self.config.romm_url}.") from e

    def play_url(self, rom_id: int) -> str:
        return self.config.romm_public_url + self.config.play_path.format(id=rom_id)

    def pool(self) -> list[dict[str, object]]:
        """Returns the playable games in the configured collection."""
        wanted = self.config.collection.casefold()
        collections = self._get("/api/collections")
        match = next(
            (c for c in collections if str(c.get("name", "")).casefold() == wanted),
            None,
        )
        if match is None:
            raise RommError(f'RomM has no collection named "{self.config.collection}".')

        page = self._get(
            "/api/roms", {"collection_id": match["id"], "limit": MAX_GAMES}
        )
        games = []
        for rom in page.get("items", []):
            if rom.get("missing_from_fs"):
                continue
            games.append(
                {
                    "id": rom["id"],
                    "name": rom.get("name") or rom.get("fs_name_no_ext") or rom["id"],
                    "platform": rom.get("platform_display_name")
                    or rom.get("platform_slug")
                    or "",
                    "playUrl": self.play_url(rom["id"]),
                }
            )
        games.sort(key=lambda g: str(g["name"]).casefold())
        return games


def pick(games: list[dict[str, object]], exclude: set[int]) -> dict[str, object]:
    """Picks a game at random, skipping excluded ids while any others remain."""
    if not games:
        raise RommError("The collection has no playable games.")
    candidates = [g for g in games if g["id"] not in exclude] or games
    return secrets.choice(candidates)


def make_handler(config: Config, romm: Romm) -> type[BaseHTTPRequestHandler]:
    static_files = {p.name: p for p in config.static_dir.iterdir() if p.is_file()}

    class Handler(BaseHTTPRequestHandler):
        server_version = "roll-page"

        def log_message(self, format: str, *args: object) -> None:
            log.info("%s %s", self.address_string(), format % args)

        def _send(self, status: int, body: bytes, content_type: str) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def _json(self, status: int, payload: object) -> None:
            self._send(status, json.dumps(payload).encode(), "application/json")

        def _pool(self) -> list[dict[str, object]] | None:
            try:
                return romm.pool()
            except RommError as e:
                log.warning("pool: %s", e)
                self._json(HTTPStatus.BAD_GATEWAY, {"error": str(e)})
                return None

        def do_GET(self) -> None:
            path = urllib.parse.urlsplit(self.path).path
            if path == "/healthz":
                self._send(HTTPStatus.OK, b"ok\n", "text/plain")
                return
            if path == "/api/pool":
                games = self._pool()
                if games is not None:
                    self._json(
                        HTTPStatus.OK,
                        {"collection": config.collection, "games": games},
                    )
                return
            name = "index.html" if path == "/" else path.removeprefix("/static/")
            file = static_files.get(name)
            if file is None or (path != "/" and not path.startswith("/static/")):
                self._json(HTTPStatus.NOT_FOUND, {"error": "not found"})
                return
            content_type = mimetypes.guess_type(name)[0] or "application/octet-stream"
            if content_type.startswith("text/") or name.endswith(".js"):
                content_type += "; charset=utf-8"
            self._send(HTTPStatus.OK, file.read_bytes(), content_type)

        def do_POST(self) -> None:
            if urllib.parse.urlsplit(self.path).path != "/api/roll":
                self._json(HTTPStatus.NOT_FOUND, {"error": "not found"})
                return
            try:
                length = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(length) or b"{}")
                exclude = {int(i) for i in body.get("exclude", [])}
            except (ValueError, TypeError, AttributeError):
                self._json(HTTPStatus.BAD_REQUEST, {"error": "bad request body"})
                return
            games = self._pool()
            if games is None:
                return
            try:
                game = pick(games, exclude)
            except RommError as e:
                self._json(HTTPStatus.BAD_GATEWAY, {"error": str(e)})
                return
            log.info("rolled %s (%s)", game["name"], game["id"])
            self._json(HTTPStatus.OK, {"game": game, "games": games})

    return Handler


def serve(config: Config) -> ThreadingHTTPServer:
    return ThreadingHTTPServer(("", config.port), make_handler(config, Romm(config)))


def main() -> None:
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    config = Config.from_env()
    server = serve(config)
    log.info("listening on :%d", server.server_address[1])
    server.serve_forever()


if __name__ == "__main__":
    main()
