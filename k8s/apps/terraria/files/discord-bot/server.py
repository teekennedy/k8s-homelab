"""Discord bot for managing a Terraria server on Kubernetes via slash commands.

Runs as an HTTP server that receives Discord Interactions via webhook.
Provides /terraria start, /terraria stop, and /terraria status commands
that scale the Terraria Kubernetes deployment.

Follows Terraria server logs to track online players and automatically
stops the server after a period of inactivity.
"""

from __future__ import annotations

import http.client
import json
import os
import re
import ssl
import sys
import threading
import time
from dataclasses import dataclass, field
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from nacl.exceptions import BadSignatureError
from nacl.signing import VerifyKey

# Discord interaction types
INTERACTION_PING = 1
INTERACTION_APPLICATION_COMMAND = 2

# Discord interaction response types
RESPONSE_PONG = 1
RESPONSE_CHANNEL_MESSAGE = 4

COMMAND_NAME = "terraria"
DISCORD_USER_AGENT = "DiscordBot (https://terraria.msng.to, 1.0)"

# Regex for Terraria join/leave log messages
JOIN_RE = re.compile(r"^(.+) has joined\.$")
LEAVE_RE = re.compile(r"^(.+) has left\.$")


@dataclass(frozen=True)
class Config:
    discord_public_key: str
    terraria_namespace: str
    terraria_deployment: str
    port: int
    interaction_path: str
    auto_stop_minutes: int
    minimum_run_minutes: int
    discord_bot_token: str
    discord_app_id: str
    discord_channel_id: str


def load_config() -> Config:
    public_key = os.environ.get("DISCORD_PUBLIC_KEY", "")
    if not public_key:
        print("ERROR: DISCORD_PUBLIC_KEY is required", file=sys.stderr)
        sys.exit(1)
    return Config(
        discord_public_key=public_key,
        terraria_namespace=os.environ.get("TERRARIA_NAMESPACE", "terraria"),
        terraria_deployment=os.environ.get("TERRARIA_DEPLOYMENT", "terraria"),
        port=int(os.environ.get("PORT", "8080")),
        interaction_path=os.environ.get("INTERACTION_PATH", "/discord"),
        auto_stop_minutes=int(os.environ.get("AUTO_STOP_MINUTES", "5")),
        minimum_run_minutes=int(os.environ.get("MINIMUM_RUN_MINUTES", "30")),
        discord_bot_token=os.environ.get("DISCORD_BOT_TOKEN", ""),
        discord_app_id=os.environ.get("DISCORD_APP_ID", ""),
        discord_channel_id=os.environ.get("DISCORD_CHANNEL_ID", ""),
    )


# --- Kubernetes API ---


def get_k8s_auth() -> tuple[str, ssl.SSLContext]:
    """Read the in-cluster service account token and CA certificate."""
    token_path = "/var/run/secrets/kubernetes.io/serviceaccount/token"
    ca_path = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
    with open(token_path) as f:
        token = f.read().strip()
    ctx = ssl.create_default_context(cafile=ca_path)
    return token, ctx


def k8s_request(method: str, path: str, body: dict | None = None) -> dict:
    """Make an authenticated request to the Kubernetes API server."""
    token, ctx = get_k8s_auth()
    url = f"https://kubernetes.default.svc{path}"
    data = json.dumps(body).encode() if body else None
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": (
            "application/strategic-merge-patch+json"
            if method == "PATCH"
            else "application/json"
        ),
    }
    req = Request(url, data=data, headers=headers, method=method)
    with urlopen(req, context=ctx) as resp:
        return json.loads(resp.read())


def k8s_log_stream(
    config: Config, pod_name: str, container: str
) -> http.client.HTTPResponse:
    """Open a streaming log connection to a pod. Caller must close the response."""
    token, ctx = get_k8s_auth()
    path = (
        f"/api/v1/namespaces/{config.terraria_namespace}"
        f"/pods/{pod_name}/log?container={container}&follow=true"
    )
    conn = http.client.HTTPSConnection("kubernetes.default.svc", context=ctx)
    conn.request("GET", path, headers={"Authorization": f"Bearer {token}"})
    resp = conn.getresponse()
    # Stash the connection on the response so we can close it later
    resp._conn = conn  # type: ignore[attr-defined]
    return resp


def find_terraria_pod(config: Config) -> str | None:
    """Find a running terraria server pod by label selector."""
    selector = f"app.kubernetes.io/controller={config.terraria_deployment}"
    path = (
        f"/api/v1/namespaces/{config.terraria_namespace}"
        f"/pods?labelSelector={selector}"
    )
    result = k8s_request("GET", path)
    for pod in result.get("items", []):
        phase = pod.get("status", {}).get("phase", "")
        if phase == "Running":
            return pod["metadata"]["name"]
    return None


def get_deployment_status(config: Config) -> dict:
    """Get the terraria deployment object from the Kubernetes API."""
    path = (
        f"/apis/apps/v1/namespaces/{config.terraria_namespace}"
        f"/deployments/{config.terraria_deployment}"
    )
    return k8s_request("GET", path)


def scale_deployment(config: Config, replicas: int) -> dict:
    """Scale the terraria deployment to the given replica count."""
    path = (
        f"/apis/apps/v1/namespaces/{config.terraria_namespace}"
        f"/deployments/{config.terraria_deployment}/scale"
    )
    return k8s_request("PATCH", path, {"spec": {"replicas": replicas}})


# --- Discord Helpers ---


def verify_signature(
    public_key_hex: str, signature: str, timestamp: str, body: bytes
) -> bool:
    """Verify a Discord interaction request signature."""
    try:
        verify_key = VerifyKey(bytes.fromhex(public_key_hex))
        verify_key.verify(timestamp.encode() + body, bytes.fromhex(signature))
        return True
    except (BadSignatureError, ValueError):
        return False


def notify_channel(config: Config, message: str):
    """Send a message to the configured Discord channel."""
    if not config.discord_bot_token or not config.discord_channel_id:
        return
    try:
        url = (
            f"https://discord.com/api/v10/channels/"
            f"{config.discord_channel_id}/messages"
        )
        data = json.dumps({"content": message}).encode()
        req = Request(
            url,
            data=data,
            headers={
                "Authorization": f"Bot {config.discord_bot_token}",
                "Content-Type": "application/json",
                "User-Agent": DISCORD_USER_AGENT,
            },
        )
        urlopen(req)
    except Exception as e:
        print(f"Failed to send Discord notification: {e}", file=sys.stderr)


# --- Log Parsing ---


def parse_log_line(line: str) -> tuple[str, str] | None:
    """Parse a Terraria server log line for join/leave events.

    Returns ("join", username) or ("leave", username) or None.
    """
    line = line.strip()
    m = JOIN_RE.match(line)
    if m:
        return ("join", m.group(1))
    m = LEAVE_RE.match(line)
    if m:
        return ("leave", m.group(1))
    return None


# --- Player Tracker ---


class PlayerTracker:
    """Follows Terraria server pod logs and tracks online players."""

    def __init__(self, config: Config):
        self.config = config
        self._players: set[str] = set()
        self._lock = threading.Lock()
        self._last_player_left: float | None = None
        self._thread: threading.Thread | None = None
        self._stop_event = threading.Event()

    @property
    def players(self) -> frozenset[str]:
        with self._lock:
            return frozenset(self._players)

    @property
    def player_count(self) -> int:
        with self._lock:
            return len(self._players)

    @property
    def last_player_left_at(self) -> float | None:
        with self._lock:
            return self._last_player_left

    def start(self):
        """Start the background log-following thread."""
        self._stop_event.clear()
        self._thread = threading.Thread(target=self._follow_logs, daemon=True)
        self._thread.start()

    def stop(self):
        """Signal the background thread to stop."""
        self._stop_event.set()
        if self._thread:
            self._thread.join(timeout=5)

    def reset(self):
        """Clear all player state (e.g. when pod disappears)."""
        with self._lock:
            self._players.clear()
            self._last_player_left = None

    def handle_event(self, event: str, username: str):
        """Process a join or leave event."""
        with self._lock:
            if event == "join":
                self._players.add(username)
            elif event == "leave":
                self._players.discard(username)
                if len(self._players) == 0:
                    self._last_player_left = time.monotonic()
        if event == "join":
            notify_channel(self.config, f"**{username}** joined the Terraria server.")
        elif event == "leave":
            notify_channel(self.config, f"**{username}** left the Terraria server.")

    def _follow_logs(self):
        """Background thread: find the terraria pod and follow its logs."""
        while not self._stop_event.is_set():
            try:
                pod_name = find_terraria_pod(self.config)
                if not pod_name:
                    self.reset()
                    print("No running terraria pod found, retrying in 10s...")
                    self._stop_event.wait(10)
                    continue
                print(f"Following logs for pod {pod_name}")
                self._stream_pod_logs(pod_name)
            except Exception as e:
                print(f"Log follower error: {e}", file=sys.stderr)
            self.reset()
            if not self._stop_event.is_set():
                self._stop_event.wait(5)

    def _stream_pod_logs(self, pod_name: str):
        """Stream logs from a terraria pod, processing join/leave events."""
        resp = k8s_log_stream(self.config, pod_name, "terraria")
        try:
            if resp.status != 200:
                print(f"Log stream returned HTTP {resp.status}", file=sys.stderr)
                return
            for line_bytes in resp:
                if self._stop_event.is_set():
                    break
                line = line_bytes.decode("utf-8", errors="replace").strip()
                if not line:
                    continue
                event = parse_log_line(line)
                if event:
                    self.handle_event(*event)
        finally:
            resp.close()
            conn = getattr(resp, "_conn", None)
            if conn:
                conn.close()


# --- Server Monitor (Auto-stop) ---


class ServerMonitor:
    """Monitors the Terraria server and auto-stops it when idle."""

    def __init__(self, config: Config, player_tracker: PlayerTracker):
        self.config = config
        self.player_tracker = player_tracker
        self._server_detected_at: float | None = None
        self._lock = threading.Lock()
        self._thread: threading.Thread | None = None
        self._stop_event = threading.Event()

    @property
    def server_uptime_minutes(self) -> int | None:
        """Approximate minutes since the server was detected running."""
        with self._lock:
            if self._server_detected_at is None:
                return None
            return int((time.monotonic() - self._server_detected_at) / 60)

    def mark_server_started(self):
        """Record that the server has been started."""
        with self._lock:
            if self._server_detected_at is None:
                self._server_detected_at = time.monotonic()

    def mark_server_stopped(self):
        """Record that the server has been stopped."""
        with self._lock:
            self._server_detected_at = None

    def start(self):
        """Start the background monitoring thread."""
        self._stop_event.clear()
        self._thread = threading.Thread(target=self._check_loop, daemon=True)
        self._thread.start()

    def stop(self):
        """Signal the monitoring thread to stop."""
        self._stop_event.set()
        if self._thread:
            self._thread.join(timeout=5)

    def _check_loop(self):
        while not self._stop_event.is_set():
            try:
                self._check_auto_stop()
            except Exception as e:
                print(f"Auto-stop check error: {e}", file=sys.stderr)
            self._stop_event.wait(30)

    def _check_auto_stop(self):
        deployment = get_deployment_status(self.config)
        replicas = deployment.get("spec", {}).get("replicas", 0)

        if replicas == 0:
            self.mark_server_stopped()
            return

        # Track when we first see the server running
        self.mark_server_started()

        uptime = self.server_uptime_minutes
        if uptime is None or uptime < self.config.minimum_run_minutes:
            return

        if self.player_tracker.player_count > 0:
            return

        last_left = self.player_tracker.last_player_left_at
        if last_left is None:
            return

        idle_seconds = time.monotonic() - last_left
        if idle_seconds < self.config.auto_stop_minutes * 60:
            return

        # All conditions met
        idle_min = int(idle_seconds / 60)
        print(
            f"Auto-stopping server: uptime={uptime}m, " f"idle={idle_min}m, players=0"
        )
        scale_deployment(self.config, 0)
        self.player_tracker.reset()
        self.mark_server_stopped()
        notify_channel(
            self.config,
            "Terraria server has been automatically stopped (no players). "
            "Use `/terraria start` to start it again.",
        )


# Module-level state set by main()
PLAYER_TRACKER: PlayerTracker | None = None
SERVER_MONITOR: ServerMonitor | None = None


# --- Command Handlers ---


def handle_start_command(config: Config) -> dict:
    """Handle the /terraria start subcommand."""
    try:
        deployment = get_deployment_status(config)
        current = deployment.get("spec", {}).get("replicas", 0)
        if current > 0:
            return {"content": "Terraria server is already running!"}
        scale_deployment(config, 1)
        if SERVER_MONITOR:
            SERVER_MONITOR.mark_server_started()
        msg = (
            "Starting Terraria server... "
            "It may take a few minutes for the world to load."
        )
        return {"content": msg}
    except Exception as e:
        return {"content": f"Failed to start server: {e}"}


def handle_stop_command(config: Config) -> dict:
    """Handle the /terraria stop subcommand."""
    try:
        deployment = get_deployment_status(config)
        current = deployment.get("spec", {}).get("replicas", 0)
        if current == 0:
            return {"content": "Terraria server is already stopped."}
        scale_deployment(config, 0)
        if SERVER_MONITOR:
            SERVER_MONITOR.mark_server_stopped()
        if PLAYER_TRACKER:
            PLAYER_TRACKER.reset()
        return {"content": "Stopping Terraria server..."}
    except Exception as e:
        return {"content": f"Failed to stop server: {e}"}


def handle_status_command(config: Config) -> dict:
    """Handle the /terraria status subcommand."""
    try:
        deployment = get_deployment_status(config)
        spec_replicas = deployment.get("spec", {}).get("replicas", 0)
        status = deployment.get("status", {})
        ready = status.get("readyReplicas") or 0

        if spec_replicas == 0:
            state = "Stopped"
        elif ready > 0:
            state = "Running"
        else:
            state = "Starting..."

        msg = (
            f"**Terraria Server Status**\n"
            f"State: {state}\n"
            f"Replicas: {ready}/{spec_replicas} ready"
        )

        if spec_replicas > 0 and PLAYER_TRACKER:
            players = PLAYER_TRACKER.players
            count = len(players)
            if players:
                names = ", ".join(sorted(players))
                msg += f"\nPlayers: {count} online ({names})"
            else:
                msg += f"\nPlayers: {count} online"

        if spec_replicas > 0 and ready > 0:
            msg += "\nConnect at: `terraria.msng.to:7777`"

        return {"content": msg}
    except Exception as e:
        return {"content": f"Failed to get status: {e}"}


def handle_interaction(config: Config, interaction: dict) -> dict:
    """Route a Discord interaction to the appropriate handler."""
    itype = interaction.get("type")
    if itype == INTERACTION_PING:
        return {"type": RESPONSE_PONG}

    if itype != INTERACTION_APPLICATION_COMMAND:
        return {
            "type": RESPONSE_CHANNEL_MESSAGE,
            "data": {"content": "Unknown interaction type."},
        }

    data = interaction.get("data", {})
    if data.get("name") != COMMAND_NAME:
        return {
            "type": RESPONSE_CHANNEL_MESSAGE,
            "data": {"content": f"Unknown command: {data.get('name')}"},
        }

    options = data.get("options", [])
    if not options:
        return {
            "type": RESPONSE_CHANNEL_MESSAGE,
            "data": {"content": "Please specify a subcommand: start, stop, or status."},
        }

    subcommand = options[0].get("name", "")
    handlers = {
        "start": handle_start_command,
        "stop": handle_stop_command,
        "status": handle_status_command,
    }
    handler = handlers.get(subcommand)
    if not handler:
        return {
            "type": RESPONSE_CHANNEL_MESSAGE,
            "data": {"content": f"Unknown subcommand: {subcommand}"},
        }

    result = handler(config)
    return {"type": RESPONSE_CHANNEL_MESSAGE, "data": result}


# --- Command Registration ---


def register_commands(config: Config):
    """Register Discord slash commands via the Discord API.

    Requires DISCORD_BOT_TOKEN and DISCORD_APP_ID to be set.
    """
    if not config.discord_bot_token or not config.discord_app_id:
        print(
            "Skipping command registration "
            "(DISCORD_BOT_TOKEN and DISCORD_APP_ID required)."
        )
        return

    url = f"https://discord.com/api/v10/applications/{config.discord_app_id}/commands"
    commands = [
        {
            "name": COMMAND_NAME,
            "type": 1,
            "description": "Manage the Terraria server",
            "options": [
                {
                    "name": "start",
                    "description": "Start the Terraria server",
                    "type": 1,
                },
                {
                    "name": "stop",
                    "description": "Stop the Terraria server",
                    "type": 1,
                },
                {
                    "name": "status",
                    "description": "Check the Terraria server status",
                    "type": 1,
                },
            ],
        }
    ]
    data = json.dumps(commands).encode()
    req = Request(
        url,
        data=data,
        headers={
            "Authorization": f"Bot {config.discord_bot_token}",
            "Content-Type": "application/json",
            "User-Agent": DISCORD_USER_AGENT,
        },
        method="PUT",
    )
    try:
        with urlopen(req) as resp:
            print(f"Registered slash commands (HTTP {resp.status}).")
    except HTTPError as e:
        print(
            f"Failed to register commands: {e.code} {e.read().decode()}",
            file=sys.stderr,
        )


# --- HTTP Server ---


class Handler(BaseHTTPRequestHandler):
    config: Config

    def do_GET(self):
        if self.path in ("/health", "/healthz"):
            self._respond(200, {"status": "ok"})
        else:
            self._respond(404, {"error": "not found"})

    def do_POST(self):
        if self.path != self.config.interaction_path:
            self._respond(404, {"error": "not found"})
            return

        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length)

        signature = self.headers.get("X-Signature-Ed25519", "")
        timestamp = self.headers.get("X-Signature-Timestamp", "")
        if not verify_signature(
            self.config.discord_public_key, signature, timestamp, body
        ):
            self._respond(401, {"error": "invalid signature"})
            return

        try:
            interaction = json.loads(body)
        except json.JSONDecodeError:
            self._respond(400, {"error": "invalid JSON"})
            return

        response = handle_interaction(self.config, interaction)
        self._respond(200, response)

    def _respond(self, status: int, data: dict):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        print(f"{self.client_address[0]} - {format % args}")


def main():
    global PLAYER_TRACKER, SERVER_MONITOR

    config = load_config()
    register_commands(config)

    player_tracker = PlayerTracker(config)
    player_tracker.start()
    PLAYER_TRACKER = player_tracker

    server_monitor = ServerMonitor(config, player_tracker)
    server_monitor.start()
    SERVER_MONITOR = server_monitor

    Handler.config = config

    server = HTTPServer(("0.0.0.0", config.port), Handler)
    print(f"Discord bot listening on port {config.port}")
    print(f"Interaction endpoint: {config.interaction_path}")
    print(
        f"Auto-stop: after {config.auto_stop_minutes}m idle "
        f"(minimum run: {config.minimum_run_minutes}m)"
    )

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        server_monitor.stop()
        player_tracker.stop()
        print("Server stopped.")


if __name__ == "__main__":
    main()
