"""Integration tests for the Terraria Discord bot.

These tests start the real HTTP server and send actual HTTP requests,
with Kubernetes API calls mocked at the function level.
"""

from __future__ import annotations

import json
import threading
from http.client import HTTPConnection
from http.server import HTTPServer
from unittest.mock import patch

import pytest
from nacl.signing import SigningKey

from server import (
    Config,
    Handler,
    PlayerTracker,
    INTERACTION_APPLICATION_COMMAND,
    INTERACTION_PING,
)

import server as server_module

# Test Ed25519 keypair
SIGNING_KEY = SigningKey.generate()
PUBLIC_KEY_HEX = SIGNING_KEY.verify_key.encode().hex()


def make_config(**overrides) -> Config:
    defaults = dict(
        discord_public_key=PUBLIC_KEY_HEX,
        terraria_namespace="terraria",
        terraria_deployment="terraria",
        port=0,  # Let OS assign a free port
        interaction_path="/discord",
        auto_stop_minutes=5,
        minimum_run_minutes=30,
        discord_bot_token="",
        discord_app_id="",
        discord_channel_id="",
    )
    defaults.update(overrides)
    return Config(**defaults)


def sign_body(body: bytes, timestamp: str = "1234567890") -> tuple[str, str]:
    """Sign a request body and return (signature_hex, timestamp)."""
    message = timestamp.encode() + body
    signed = SIGNING_KEY.sign(message)
    return signed.signature.hex(), timestamp


class BotServer:
    """Context manager that runs the discord bot HTTP server in a background thread."""

    def __init__(self, config: Config, player_tracker: PlayerTracker | None = None):
        self.config = config
        self.player_tracker = player_tracker
        self.server: HTTPServer | None = None
        self.thread: threading.Thread | None = None
        self._old_tracker = None
        self._old_monitor = None

    def __enter__(self):
        Handler.config = self.config
        # Set module-level PLAYER_TRACKER for status command
        self._old_tracker = server_module.PLAYER_TRACKER
        self._old_monitor = server_module.SERVER_MONITOR
        server_module.PLAYER_TRACKER = self.player_tracker
        server_module.SERVER_MONITOR = None
        self.server = HTTPServer(("127.0.0.1", self.config.port), Handler)
        self.port = self.server.server_address[1]
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.daemon = True
        self.thread.start()
        return self

    def __exit__(self, *args):
        if self.server:
            self.server.shutdown()
            self.server.server_close()
        server_module.PLAYER_TRACKER = self._old_tracker
        server_module.SERVER_MONITOR = self._old_monitor

    def request(
        self,
        method: str,
        path: str,
        body: bytes | None = None,
        headers: dict | None = None,
    ) -> tuple[int, dict]:
        conn = HTTPConnection("127.0.0.1", self.port)
        conn.request(method, path, body=body, headers=headers or {})
        resp = conn.getresponse()
        data = json.loads(resp.read())
        status = resp.status
        conn.close()
        return status, data

    def discord_request(self, interaction: dict) -> tuple[int, dict]:
        """Send a properly signed Discord interaction request."""
        body = json.dumps(interaction).encode()
        sig, ts = sign_body(body)
        headers = {
            "Content-Type": "application/json",
            "Content-Length": str(len(body)),
            "X-Signature-Ed25519": sig,
            "X-Signature-Timestamp": ts,
        }
        return self.request("POST", self.config.interaction_path, body, headers)


@pytest.mark.integration
class TestHealthEndpoints:
    def test_health(self):
        with BotServer(make_config()) as srv:
            status, data = srv.request("GET", "/health")
            assert status == 200
            assert data["status"] == "ok"

    def test_healthz(self):
        with BotServer(make_config()) as srv:
            status, data = srv.request("GET", "/healthz")
            assert status == 200
            assert data["status"] == "ok"

    def test_not_found(self):
        with BotServer(make_config()) as srv:
            status, data = srv.request("GET", "/nonexistent")
            assert status == 404


@pytest.mark.integration
class TestSignatureVerification:
    def test_valid_signature(self):
        with BotServer(make_config()) as srv:
            status, data = srv.discord_request({"type": INTERACTION_PING})
            assert status == 200

    def test_invalid_signature(self):
        with BotServer(make_config()) as srv:
            body = json.dumps({"type": INTERACTION_PING}).encode()
            headers = {
                "Content-Type": "application/json",
                "Content-Length": str(len(body)),
                "X-Signature-Ed25519": "00" * 64,
                "X-Signature-Timestamp": "ts",
            }
            status, data = srv.request("POST", "/discord", body, headers)
            assert status == 401

    def test_missing_signature(self):
        with BotServer(make_config()) as srv:
            body = json.dumps({"type": INTERACTION_PING}).encode()
            headers = {
                "Content-Type": "application/json",
                "Content-Length": str(len(body)),
            }
            status, data = srv.request("POST", "/discord", body, headers)
            assert status == 401


@pytest.mark.integration
class TestPingPong:
    def test_ping_returns_pong(self):
        with BotServer(make_config()) as srv:
            status, data = srv.discord_request({"type": INTERACTION_PING})
            assert status == 200
            assert data["type"] == 1  # PONG


@pytest.mark.integration
class TestStartCommand:
    @patch("server.scale_deployment", return_value={})
    @patch(
        "server.get_deployment_status",
        return_value={"spec": {"replicas": 0}},
    )
    def test_start_stopped_server(self, mock_get, mock_scale):
        with BotServer(make_config()) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "start", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            assert "Starting" in data["data"]["content"]
            mock_scale.assert_called_once()

    @patch(
        "server.get_deployment_status",
        return_value={"spec": {"replicas": 1}},
    )
    def test_start_already_running(self, mock_get):
        with BotServer(make_config()) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "start", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            assert "already running" in data["data"]["content"]


@pytest.mark.integration
class TestStopCommand:
    @patch("server.scale_deployment", return_value={})
    @patch(
        "server.get_deployment_status",
        return_value={"spec": {"replicas": 1}},
    )
    def test_stop_running_server(self, mock_get, mock_scale):
        with BotServer(make_config()) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "stop", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            assert "Stopping" in data["data"]["content"]
            mock_scale.assert_called_once()

    @patch(
        "server.get_deployment_status",
        return_value={"spec": {"replicas": 0}},
    )
    def test_stop_already_stopped(self, mock_get):
        with BotServer(make_config()) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "stop", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            assert "already stopped" in data["data"]["content"]


@pytest.mark.integration
class TestStatusCommand:
    @patch(
        "server.get_deployment_status",
        return_value={
            "spec": {"replicas": 1},
            "status": {"readyReplicas": 1, "availableReplicas": 1},
        },
    )
    def test_status_running_with_players(self, mock_get):
        config = make_config()
        tracker = PlayerTracker(config)
        with patch("server.notify_channel"):
            tracker.handle_event("join", "Steve")
            tracker.handle_event("join", "Alex")
        with BotServer(config, player_tracker=tracker) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "status", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            content = data["data"]["content"]
            assert "Running" in content
            assert "2 online" in content
            assert "Alex" in content
            assert "Steve" in content

    @patch(
        "server.get_deployment_status",
        return_value={
            "spec": {"replicas": 1},
            "status": {"readyReplicas": 1, "availableReplicas": 1},
        },
    )
    def test_status_running_no_players(self, mock_get):
        config = make_config()
        tracker = PlayerTracker(config)
        with BotServer(config, player_tracker=tracker) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "status", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            content = data["data"]["content"]
            assert "Running" in content
            assert "0 online" in content

    @patch(
        "server.get_deployment_status",
        return_value={"spec": {"replicas": 0}, "status": {}},
    )
    def test_status_stopped(self, mock_get):
        with BotServer(make_config()) as srv:
            interaction = {
                "type": INTERACTION_APPLICATION_COMMAND,
                "data": {
                    "name": "terraria",
                    "options": [{"name": "status", "type": 1}],
                },
            }
            status, data = srv.discord_request(interaction)
            assert status == 200
            assert "Stopped" in data["data"]["content"]


@pytest.mark.integration
class TestWrongPath:
    def test_post_wrong_path(self):
        with BotServer(make_config()) as srv:
            body = b'{"type":1}'
            sig, ts = sign_body(body)
            headers = {
                "Content-Type": "application/json",
                "Content-Length": str(len(body)),
                "X-Signature-Ed25519": sig,
                "X-Signature-Timestamp": ts,
            }
            status, data = srv.request("POST", "/wrong", body, headers)
            assert status == 404
