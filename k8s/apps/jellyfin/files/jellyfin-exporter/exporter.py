# /// script
# requires-python = ">=3.12"
# dependencies = ["websockets==17.0.1"]
# ///
"""Prometheus exporter for Jellyfin playback sessions.

Jellyfin reports an audio-only transcode and a full video encode both as
"Transcode". They cost wildly different amounts: passing 1080p H264 video
through untouched while converting EAC3 5.1 to stereo AAC uses very little CPU
and no GPU at all, whereas a real video encode does. This exporter keeps them
apart so dashboards and alerts can too.

State is fed by Jellyfin's /socket WebSocket (the same feed Jellystat uses) for
low latency, with a slower GET /Sessions reconcile as drift correction and as a
fallback when the socket flaps. Metrics are served from whatever the last
successful update produced.

Deployed as a single file and run with `uv run --script`, which reads the
PEP 723 header above to build its environment. Keep that header's pin in step
with the sibling pyproject.toml, which is what the tests resolve against.
"""

from __future__ import annotations

import json
import logging
import os
import sys
import threading
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Iterable

from websockets.sync.client import ClientConnection, connect

log = logging.getLogger("jellyfin-exporter")


# --------------------------------------------------------------------------
# Session classification
# --------------------------------------------------------------------------


@dataclass(frozen=True)
class Session:
    user: str
    client: str
    device: str
    playing: bool
    paused: bool
    play_method: str
    video_transcode: bool
    audio_transcode: bool
    hardware_acceleration: str
    transcode_reasons: str


def _text(value: Any, default: str = "unknown") -> str:
    if value is None or value == "":
        return default
    return str(value)


def _reasons(raw: Any) -> str:
    """Normalise TranscodeReasons, which may be a list or a flags string."""
    if not raw:
        return ""
    if isinstance(raw, str):
        parts = [p.strip() for p in raw.split(",")]
    else:
        parts = [str(p).strip() for p in raw]
    return ",".join(sorted(p for p in parts if p))


def classify(raw: dict[str, Any]) -> Session:
    """Turn a SessionInfoDto into the handful of facts we actually export."""
    play_state = raw.get("PlayState") or {}
    transcoding = raw.get("TranscodingInfo") or {}

    playing = raw.get("NowPlayingItem") is not None

    # A session can report PlayMethod=Transcode for a moment before
    # TranscodingInfo is populated. Treat the absence as "no transcode"
    # rather than inventing GPU load that may not exist.
    video_transcode = bool(transcoding) and not transcoding.get("IsVideoDirect", False)
    audio_transcode = bool(transcoding) and not transcoding.get("IsAudioDirect", False)

    return Session(
        user=_text(raw.get("UserName")),
        client=_text(raw.get("Client")),
        device=_text(raw.get("DeviceName")),
        playing=playing,
        paused=bool(play_state.get("IsPaused", False)),
        play_method=_text(play_state.get("PlayMethod"), default=""),
        video_transcode=video_transcode,
        audio_transcode=audio_transcode,
        hardware_acceleration=_text(
            transcoding.get("HardwareAccelerationType"), default=""
        ),
        transcode_reasons=_reasons(transcoding.get("TranscodeReasons")),
    )


# --------------------------------------------------------------------------
# Prometheus rendering
# --------------------------------------------------------------------------

_GAUGES = (
    (
        "jellyfin_active_streams",
        "Sessions currently playing and not paused.",
    ),
    (
        "jellyfin_paused_streams",
        "Sessions with media loaded but paused.",
    ),
    (
        "jellyfin_active_video_transcodes",
        "Active unpaused sessions transcoding video (the ones that cost GPU).",
    ),
    (
        "jellyfin_active_audio_transcodes",
        "Active unpaused sessions transcoding audio (cheap; video may be direct).",
    ),
)


def _escape(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def _labels(pairs: Iterable[tuple[str, str]]) -> str:
    return ",".join(f'{k}="{_escape(v)}"' for k, v in pairs)


def render(
    raw_sessions: Iterable[dict[str, Any]], *, up: bool, last_success: float
) -> str:
    """Render exposition text.

    When `up` is false the session-derived gauges are reported as zero
    rather than replaying stale state. A stuck exporter must not hold the
    kured reboot gate shut indefinitely; `jellyfin_up` is what tells you
    the difference between "nobody is watching" and "we cannot tell".
    """
    sessions = [classify(s) for s in raw_sessions] if up else []
    playing = [s for s in sessions if s.playing]
    active = [s for s in playing if not s.paused]

    counts = {
        "jellyfin_active_streams": len(active),
        "jellyfin_paused_streams": len([s for s in playing if s.paused]),
        "jellyfin_active_video_transcodes": len(
            [s for s in active if s.video_transcode]
        ),
        "jellyfin_active_audio_transcodes": len(
            [s for s in active if s.audio_transcode]
        ),
    }

    lines: list[str] = []
    for name, help_text in _GAUGES:
        lines.append(f"# HELP {name} {help_text}")
        lines.append(f"# TYPE {name} gauge")
        lines.append(f"{name} {counts[name]}")

    lines.append(
        "# HELP jellyfin_up Whether the last Jellyfin session query succeeded."
    )
    lines.append("# TYPE jellyfin_up gauge")
    lines.append(f"jellyfin_up {1 if up else 0}")

    lines.append(
        "# HELP jellyfin_exporter_last_success_timestamp_seconds "
        "Unix time of the last successful session query."
    )
    lines.append("# TYPE jellyfin_exporter_last_success_timestamp_seconds gauge")
    lines.append(f"jellyfin_exporter_last_success_timestamp_seconds {last_success}")

    if playing:
        lines.append(
            "# HELP jellyfin_session_info Labelled detail for each playing session."
        )
        lines.append("# TYPE jellyfin_session_info gauge")
        for s in playing:
            # Deliberately no item name: titles churn constantly and would
            # make this series unbounded over time.
            labels = _labels(
                (
                    ("user", s.user),
                    ("client", s.client),
                    ("device", s.device),
                    ("play_method", s.play_method),
                    ("paused", "true" if s.paused else "false"),
                    ("video_transcode", "true" if s.video_transcode else "false"),
                    ("audio_transcode", "true" if s.audio_transcode else "false"),
                    ("hardware_acceleration", s.hardware_acceleration),
                    ("transcode_reasons", s.transcode_reasons),
                )
            )
            lines.append(f"jellyfin_session_info{{{labels}}} 1")

    return "\n".join(lines) + "\n"


# --------------------------------------------------------------------------
# WebSocket addressing
# --------------------------------------------------------------------------


def socket_url(base_url: str, api_key: str, device_id: str) -> str:
    parts = urllib.parse.urlsplit(base_url)
    scheme = "wss" if parts.scheme == "https" else "ws"
    query = urllib.parse.urlencode({"api_key": api_key, "deviceId": device_id})
    return urllib.parse.urlunsplit((scheme, parts.netloc, "/socket", query, ""))


def sessions_start_message(due_ms: int = 0, period_ms: int = 1500) -> dict[str, str]:
    """BasePeriodicWebSocketListener splits Data on "," into due time and period."""
    return {"MessageType": "SessionsStart", "Data": f"{due_ms},{period_ms}"}


# --------------------------------------------------------------------------
# State
# --------------------------------------------------------------------------


class State:
    """Last known sessions, with an expiry.

    Both the WebSocket feed and the REST reconcile write here. Readers get
    a snapshot plus whether it is fresh enough to trust.
    """

    def __init__(self, stale_after: float = 90.0, clock=time.time):
        self._stale_after = stale_after
        self._clock = clock
        self._lock = threading.Lock()
        self._sessions: list[dict[str, Any]] = []
        self._last_success: float = 0.0

    def update(self, sessions: Iterable[dict[str, Any]]) -> None:
        with self._lock:
            self._sessions = list(sessions)
            self._last_success = self._clock()

    def snapshot(self) -> tuple[list[dict[str, Any]], bool, float]:
        with self._lock:
            fresh = (
                self._last_success > 0
                and (self._clock() - self._last_success) <= self._stale_after
            )
            return list(self._sessions), fresh, self._last_success


# --------------------------------------------------------------------------
# Jellyfin REST client
# --------------------------------------------------------------------------


class JellyfinClient:
    def __init__(self, base_url: str, api_key: str, timeout: float = 10.0):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.timeout = timeout

    def get_sessions(self) -> list[dict[str, Any]]:
        request = urllib.request.Request(
            f"{self.base_url}/Sessions",
            # Passing the key as a header keeps it out of Jellyfin's
            # request log, unlike the api_key query parameter.
            headers={"X-Emby-Token": self.api_key, "Accept": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=self.timeout) as response:
            return json.loads(response.read().decode("utf-8"))


# --------------------------------------------------------------------------
# WebSocket feed
# --------------------------------------------------------------------------


def connect_socket(
    base_url: str, api_key: str, device_id: str, timeout: float = 30.0
) -> ClientConnection:
    """Open /socket, returning a connected client.

    Raises if Jellyfin refuses the upgrade, which is what a bad API key
    looks like from here.

    RFC ping/pong keepalives are the library's job: it pings every 20s and
    tears the connection down if a pong does not come back, so a silently
    dead socket surfaces as a reconnect rather than as a feed that never
    updates again. Jellyfin's own application-level keepalive is separate
    and handled in pump_socket.
    """
    return connect(
        socket_url(base_url, api_key, device_id),
        open_timeout=timeout,
        # Never route an in-cluster hop through a proxy picked up from the
        # environment; jellyfin-main is a Service name, not an internet host.
        proxy=None,
    )


def pump_socket(conn: ClientConnection, state: State, on_sessions=None) -> None:
    """Subscribe to session updates and feed State until the socket dies."""
    conn.send(json.dumps(sessions_start_message()))

    for raw in conn:
        if isinstance(raw, bytes):
            continue

        try:
            message = json.loads(raw)
        except ValueError:
            log.warning("ignoring malformed websocket message")
            continue

        message_type = message.get("MessageType")

        # Jellyfin asks idle clients to prove they are alive. This is a JSON
        # message of Jellyfin's own, not an RFC 6455 ping, so the library
        # does not answer it for us -- and Jellyfin drops clients that
        # ignore it.
        if message_type == "ForceKeepAlive":
            conn.send(json.dumps({"MessageType": "KeepAlive"}))
            continue

        if message_type == "Sessions":
            sessions = message.get("Data") or []
            state.update(sessions)
            if on_sessions:
                on_sessions(sessions)


def websocket_loop(
    base_url: str,
    api_key: str,
    device_id: str,
    state: State,
    stop: threading.Event,
    retry_seconds: float = 10.0,
) -> None:
    while not stop.is_set():
        try:
            with connect_socket(base_url, api_key, device_id) as conn:
                log.info("websocket connected")
                pump_socket(conn, state)
            log.warning("websocket closed by server")
        except Exception as exc:  # noqa: BLE001 - reconnect on anything
            log.warning("websocket disconnected: %s", exc)
        stop.wait(retry_seconds)


def reconcile_loop(
    client: JellyfinClient,
    state: State,
    stop: threading.Event,
    interval: float = 60.0,
) -> None:
    """Periodic REST poll.

    Cheap drift correction, and the reason a flapping WebSocket does not
    take the exporter down with it.
    """
    while not stop.is_set():
        try:
            state.update(client.get_sessions())
        except Exception as exc:  # noqa: BLE001 - keep polling
            log.warning("session reconcile failed: %s", exc)
        stop.wait(interval)


# --------------------------------------------------------------------------
# HTTP server
# --------------------------------------------------------------------------


def build_metrics_server(host: str, port: int, state: State) -> ThreadingHTTPServer:
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def do_GET(self) -> None:
            path = urllib.parse.urlsplit(self.path).path
            if path == "/metrics":
                sessions, up, last_success = state.snapshot()
                body = render(sessions, up=up, last_success=last_success).encode()
                self._respond(200, "text/plain; version=0.0.4; charset=utf-8", body)
            elif path == "/healthz":
                # Liveness reflects this process only.
                self._respond(200, "text/plain; charset=utf-8", b"ok\n")
            else:
                self._respond(404, "text/plain; charset=utf-8", b"not found\n")

        def _respond(self, status: int, content_type: str, body: bytes) -> None:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *_: Any) -> None:
            return

    return ThreadingHTTPServer((host, port), Handler)


# --------------------------------------------------------------------------
# Entry point
# --------------------------------------------------------------------------


def start_collectors(
    base_url: str,
    api_key: str,
    device_id: str,
    state: State,
    stop: threading.Event,
    reconcile_interval: float = 60.0,
) -> bool:
    """Start the WebSocket and reconcile threads, if we have credentials.

    Returns False without starting anything when the URL or API key is
    missing. The caller still serves /metrics in that case -- see main().
    """
    if not base_url or not api_key:
        log.error(
            "JELLYFIN_URL and JELLYFIN_API_KEY are required; "
            "serving jellyfin_up 0 until they are set"
        )
        return False

    client = JellyfinClient(base_url, api_key)

    # Seed synchronously so the first scrape is not a spurious jellyfin_up 0.
    try:
        state.update(client.get_sessions())
    except Exception as exc:  # noqa: BLE001
        log.warning("initial session query failed: %s", exc)

    threading.Thread(
        target=websocket_loop,
        args=(base_url, api_key, device_id, state, stop),
        daemon=True,
    ).start()
    threading.Thread(
        target=reconcile_loop,
        args=(client, state, stop, reconcile_interval),
        daemon=True,
    ).start()
    return True


def main(serve=None) -> int:
    """Serve metrics.

    `serve` is injected by the tests; it defaults to serving forever.
    """
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(message)s",
        stream=sys.stdout,
    )

    base_url = os.environ.get("JELLYFIN_URL", "").strip()
    api_key = os.environ.get("JELLYFIN_API_KEY", "").strip()
    listen_host = os.environ.get("LISTEN_HOST", "0.0.0.0")
    listen_port = int(os.environ.get("LISTEN_PORT", "9101"))
    device_id = os.environ.get("DEVICE_ID", "jellyfin-exporter")
    reconcile_interval = float(os.environ.get("RECONCILE_INTERVAL", "60"))

    # Must outlive the reconcile interval, or every poll gap looks like an
    # outage and the reboot gate flaps.
    stale_after = float(
        os.environ.get("STALE_AFTER", str(max(90.0, reconcile_interval * 2.5)))
    )

    state = State(stale_after=stale_after)
    stop = threading.Event()
    start_collectors(base_url, api_key, device_id, state, stop, reconcile_interval)

    server = build_metrics_server(listen_host, listen_port, state)
    log.info("serving metrics on %s:%s", listen_host, server.server_port)
    if serve is None:

        def serve(target: ThreadingHTTPServer) -> None:
            target.serve_forever()

    try:
        serve(server)
    except KeyboardInterrupt:
        pass
    finally:
        stop.set()
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
