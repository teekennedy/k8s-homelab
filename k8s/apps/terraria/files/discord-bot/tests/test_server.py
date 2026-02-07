"""Unit tests for the Terraria Discord bot server."""

from __future__ import annotations

import os
import time
from unittest.mock import MagicMock, patch
from urllib.error import HTTPError

import pytest

from server import (
    COMMAND_NAME,
    INTERACTION_APPLICATION_COMMAND,
    INTERACTION_PING,
    RESPONSE_CHANNEL_MESSAGE,
    RESPONSE_PONG,
    Config,
    PlayerTracker,
    ServerMonitor,
    handle_interaction,
    handle_start_command,
    handle_status_command,
    handle_stop_command,
    load_config,
    notify_channel,
    parse_log_line,
    register_commands,
    verify_signature,
)

from nacl.signing import SigningKey

TEST_SIGNING_KEY = SigningKey.generate()
TEST_VERIFY_KEY = TEST_SIGNING_KEY.verify_key
TEST_PUBLIC_KEY_HEX = TEST_VERIFY_KEY.encode().hex()


def make_config(**overrides) -> Config:
    defaults = dict(
        discord_public_key=TEST_PUBLIC_KEY_HEX,
        terraria_namespace="terraria",
        terraria_deployment="terraria",
        port=8080,
        interaction_path="/discord",
        auto_stop_minutes=5,
        minimum_run_minutes=30,
        discord_bot_token="",
        discord_app_id="",
        discord_channel_id="",
    )
    defaults.update(overrides)
    return Config(**defaults)


def make_command_interaction(subcommand: str) -> dict:
    return {
        "type": INTERACTION_APPLICATION_COMMAND,
        "data": {
            "name": COMMAND_NAME,
            "options": [{"name": subcommand, "type": 1}],
        },
    }


# --- Config Tests ---


class TestLoadConfig:
    def test_defaults(self):
        env = {"DISCORD_PUBLIC_KEY": "abc123"}
        with patch.dict(os.environ, env, clear=True):
            config = load_config()
        assert config.discord_public_key == "abc123"
        assert config.terraria_namespace == "terraria"
        assert config.terraria_deployment == "terraria"
        assert config.port == 8080
        assert config.interaction_path == "/discord"
        assert config.auto_stop_minutes == 5
        assert config.minimum_run_minutes == 30

    def test_custom_values(self):
        env = {
            "DISCORD_PUBLIC_KEY": "key",
            "TERRARIA_NAMESPACE": "game",
            "TERRARIA_DEPLOYMENT": "my-terraria",
            "PORT": "9090",
            "INTERACTION_PATH": "/webhook",
            "AUTO_STOP_MINUTES": "60",
            "MINIMUM_RUN_MINUTES": "15",
            "DISCORD_BOT_TOKEN": "token",
            "DISCORD_APP_ID": "appid",
            "DISCORD_CHANNEL_ID": "chanid",
        }
        with patch.dict(os.environ, env, clear=True):
            config = load_config()
        assert config.terraria_namespace == "game"
        assert config.terraria_deployment == "my-terraria"
        assert config.port == 9090
        assert config.interaction_path == "/webhook"
        assert config.auto_stop_minutes == 60
        assert config.minimum_run_minutes == 15
        assert config.discord_bot_token == "token"
        assert config.discord_app_id == "appid"
        assert config.discord_channel_id == "chanid"

    def test_missing_public_key_exits(self):
        with patch.dict(os.environ, {}, clear=True):
            with pytest.raises(SystemExit):
                load_config()


# --- Signature Verification Tests ---


class TestVerifySignature:
    def test_valid_signature(self):
        timestamp = "1234567890"
        body = b'{"type":1}'
        message = timestamp.encode() + body
        signed = TEST_SIGNING_KEY.sign(message)
        signature_hex = signed.signature.hex()
        assert verify_signature(TEST_PUBLIC_KEY_HEX, signature_hex, timestamp, body)

    def test_invalid_signature(self):
        assert not verify_signature(TEST_PUBLIC_KEY_HEX, "00" * 64, "ts", b"body")

    def test_malformed_signature(self):
        assert not verify_signature(TEST_PUBLIC_KEY_HEX, "not-hex", "ts", b"body")

    def test_malformed_public_key(self):
        assert not verify_signature("bad-key", "00" * 64, "ts", b"body")


# --- Log Parsing Tests ---


class TestParseLogLine:
    def test_join(self):
        assert parse_log_line("Steve has joined.") == ("join", "Steve")

    def test_leave(self):
        assert parse_log_line("Steve has left.") == ("leave", "Steve")

    def test_join_with_spaces(self):
        assert parse_log_line("Cool Player has joined.") == ("join", "Cool Player")

    def test_leave_with_spaces(self):
        assert parse_log_line("Cool Player has left.") == ("leave", "Cool Player")

    def test_unrelated_line(self):
        assert parse_log_line("Server started on port 7777") is None

    def test_empty_line(self):
        assert parse_log_line("") is None

    def test_whitespace_stripped(self):
        assert parse_log_line("  Steve has joined.  ") == ("join", "Steve")

    def test_partial_match(self):
        assert parse_log_line("Steve has joined") is None
        assert parse_log_line("has joined.") is None


# --- Player Tracker Tests ---


class TestPlayerTracker:
    def test_initial_state(self):
        tracker = PlayerTracker(make_config())
        assert tracker.player_count == 0
        assert tracker.players == frozenset()
        assert tracker.last_player_left_at is None

    @patch("server.notify_channel")
    def test_join_adds_player(self, mock_notify):
        tracker = PlayerTracker(make_config())
        tracker.handle_event("join", "Steve")
        assert tracker.player_count == 1
        assert "Steve" in tracker.players

    @patch("server.notify_channel")
    def test_leave_removes_player(self, mock_notify):
        tracker = PlayerTracker(make_config())
        tracker.handle_event("join", "Steve")
        tracker.handle_event("leave", "Steve")
        assert tracker.player_count == 0
        assert tracker.last_player_left_at is not None

    @patch("server.notify_channel")
    def test_multiple_players(self, mock_notify):
        tracker = PlayerTracker(make_config())
        tracker.handle_event("join", "Steve")
        tracker.handle_event("join", "Alex")
        assert tracker.player_count == 2
        tracker.handle_event("leave", "Steve")
        assert tracker.player_count == 1
        # last_player_left should NOT be set since Alex is still on
        assert tracker.last_player_left_at is None

    @patch("server.notify_channel")
    def test_last_player_left_set_when_empty(self, mock_notify):
        tracker = PlayerTracker(make_config())
        tracker.handle_event("join", "Steve")
        tracker.handle_event("join", "Alex")
        tracker.handle_event("leave", "Steve")
        assert tracker.last_player_left_at is None  # Alex still on
        tracker.handle_event("leave", "Alex")
        assert tracker.last_player_left_at is not None  # Now empty

    @patch("server.notify_channel")
    def test_leave_unknown_player(self, mock_notify):
        tracker = PlayerTracker(make_config())
        tracker.handle_event("leave", "Nobody")
        assert tracker.player_count == 0

    def test_reset_clears_state(self):
        tracker = PlayerTracker(make_config())
        with patch("server.notify_channel"):
            tracker.handle_event("join", "Steve")
        tracker.reset()
        assert tracker.player_count == 0
        assert tracker.last_player_left_at is None

    @patch("server.notify_channel")
    def test_join_notifies_channel(self, mock_notify):
        config = make_config(discord_bot_token="tok", discord_channel_id="ch")
        tracker = PlayerTracker(config)
        tracker.handle_event("join", "Steve")
        mock_notify.assert_called_once()
        assert "Steve" in mock_notify.call_args[0][1]
        assert "joined" in mock_notify.call_args[0][1]

    @patch("server.notify_channel")
    def test_leave_notifies_channel(self, mock_notify):
        config = make_config(discord_bot_token="tok", discord_channel_id="ch")
        tracker = PlayerTracker(config)
        tracker.handle_event("leave", "Steve")
        mock_notify.assert_called_once()
        assert "Steve" in mock_notify.call_args[0][1]
        assert "left" in mock_notify.call_args[0][1]


# --- Server Monitor Tests ---


class TestServerMonitor:
    def _make_monitor(self, **config_overrides):
        config = make_config(**config_overrides)
        tracker = PlayerTracker(config)
        return ServerMonitor(config, tracker), tracker

    def test_initial_state(self):
        monitor, _ = self._make_monitor()
        assert monitor.server_uptime_minutes is None

    def test_mark_started(self):
        monitor, _ = self._make_monitor()
        monitor.mark_server_started()
        assert monitor.server_uptime_minutes is not None
        assert monitor.server_uptime_minutes == 0

    def test_mark_stopped(self):
        monitor, _ = self._make_monitor()
        monitor.mark_server_started()
        monitor.mark_server_stopped()
        assert monitor.server_uptime_minutes is None

    def test_mark_started_idempotent(self):
        monitor, _ = self._make_monitor()
        monitor.mark_server_started()
        first = monitor._server_detected_at
        monitor.mark_server_started()
        assert monitor._server_detected_at == first

    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_no_auto_stop_when_server_off(self, mock_get, mock_scale):
        monitor, _ = self._make_monitor()
        mock_get.return_value = {"spec": {"replicas": 0}}
        monitor._check_auto_stop()
        mock_scale.assert_not_called()

    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_no_auto_stop_before_minimum_run(self, mock_get, mock_scale):
        monitor, _ = self._make_monitor(minimum_run_minutes=30)
        mock_get.return_value = {"spec": {"replicas": 1}}
        monitor._check_auto_stop()  # Sets _server_detected_at
        monitor._check_auto_stop()  # Checks, but uptime < 30m
        mock_scale.assert_not_called()

    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_no_auto_stop_with_players(self, mock_get, mock_scale):
        monitor, tracker = self._make_monitor(minimum_run_minutes=0)
        mock_get.return_value = {"spec": {"replicas": 1}}
        with patch("server.notify_channel"):
            tracker.handle_event("join", "Steve")
        # Fake long uptime
        monitor._server_detected_at = time.monotonic() - 3600
        monitor._check_auto_stop()
        mock_scale.assert_not_called()

    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_no_auto_stop_when_no_one_has_left(self, mock_get, mock_scale):
        monitor, _ = self._make_monitor(minimum_run_minutes=0, auto_stop_minutes=5)
        mock_get.return_value = {"spec": {"replicas": 1}}
        monitor._server_detected_at = time.monotonic() - 3600
        monitor._check_auto_stop()
        mock_scale.assert_not_called()

    @patch("server.notify_channel")
    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_auto_stop_when_idle(self, mock_get, mock_scale, mock_notify):
        monitor, tracker = self._make_monitor(
            minimum_run_minutes=0, auto_stop_minutes=5
        )
        mock_get.return_value = {"spec": {"replicas": 1}}
        monitor._server_detected_at = time.monotonic() - 3600
        # Simulate: player joined and left long ago
        with patch("server.notify_channel"):
            tracker.handle_event("join", "Steve")
            tracker.handle_event("leave", "Steve")
        # Fake the last_player_left to be 10 minutes ago
        tracker._last_player_left = time.monotonic() - 600
        monitor._check_auto_stop()
        mock_scale.assert_called_once_with(monitor.config, 0)

    @patch("server.notify_channel")
    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_no_auto_stop_when_recently_idle(self, mock_get, mock_scale, mock_notify):
        monitor, tracker = self._make_monitor(
            minimum_run_minutes=0, auto_stop_minutes=5
        )
        mock_get.return_value = {"spec": {"replicas": 1}}
        monitor._server_detected_at = time.monotonic() - 3600
        with patch("server.notify_channel"):
            tracker.handle_event("join", "Steve")
            tracker.handle_event("leave", "Steve")
        # last_player_left is just now - not idle long enough
        monitor._check_auto_stop()
        mock_scale.assert_not_called()


# --- Command Handler Tests ---


class TestHandleStartCommand:
    @patch("server.SERVER_MONITOR", None)
    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_start_stopped_server(self, mock_get, mock_scale):
        mock_get.return_value = {"spec": {"replicas": 0}}
        mock_scale.return_value = {}
        config = make_config()
        result = handle_start_command(config)
        assert "Starting" in result["content"]
        mock_scale.assert_called_once_with(config, 1)

    @patch("server.get_deployment_status")
    def test_start_already_running(self, mock_get):
        mock_get.return_value = {"spec": {"replicas": 1}}
        result = handle_start_command(make_config())
        assert "already running" in result["content"]

    @patch("server.get_deployment_status", side_effect=Exception("k8s down"))
    def test_start_k8s_error(self, mock_get):
        result = handle_start_command(make_config())
        assert "Failed" in result["content"]
        assert "k8s down" in result["content"]


class TestHandleStopCommand:
    @patch("server.PLAYER_TRACKER", None)
    @patch("server.SERVER_MONITOR", None)
    @patch("server.scale_deployment")
    @patch("server.get_deployment_status")
    def test_stop_running_server(self, mock_get, mock_scale):
        mock_get.return_value = {"spec": {"replicas": 1}}
        mock_scale.return_value = {}
        config = make_config()
        result = handle_stop_command(config)
        assert "Stopping" in result["content"]
        mock_scale.assert_called_once_with(config, 0)

    @patch("server.get_deployment_status")
    def test_stop_already_stopped(self, mock_get):
        mock_get.return_value = {"spec": {"replicas": 0}}
        result = handle_stop_command(make_config())
        assert "already stopped" in result["content"]

    @patch("server.get_deployment_status", side_effect=Exception("timeout"))
    def test_stop_k8s_error(self, mock_get):
        result = handle_stop_command(make_config())
        assert "Failed" in result["content"]


class TestHandleStatusCommand:
    @patch("server.PLAYER_TRACKER", None)
    @patch("server.get_deployment_status")
    def test_status_stopped(self, mock_get):
        mock_get.return_value = {"spec": {"replicas": 0}, "status": {}}
        result = handle_status_command(make_config())
        assert "Stopped" in result["content"]
        assert "0/0" in result["content"]

    @patch("server.get_deployment_status")
    def test_status_running_with_players(self, mock_get):
        mock_get.return_value = {
            "spec": {"replicas": 1},
            "status": {"readyReplicas": 1, "availableReplicas": 1},
        }
        tracker = PlayerTracker(make_config())
        with patch("server.notify_channel"):
            tracker.handle_event("join", "Steve")
            tracker.handle_event("join", "Alex")
        with patch("server.PLAYER_TRACKER", tracker):
            result = handle_status_command(make_config())
        assert "Running" in result["content"]
        assert "2 online" in result["content"]
        assert "Alex" in result["content"]
        assert "Steve" in result["content"]
        assert "terraria.msng.to:7777" in result["content"]

    @patch("server.get_deployment_status")
    def test_status_running_no_players(self, mock_get):
        mock_get.return_value = {
            "spec": {"replicas": 1},
            "status": {"readyReplicas": 1, "availableReplicas": 1},
        }
        tracker = PlayerTracker(make_config())
        with patch("server.PLAYER_TRACKER", tracker):
            result = handle_status_command(make_config())
        assert "Running" in result["content"]
        assert "0 online" in result["content"]

    @patch("server.PLAYER_TRACKER", None)
    @patch("server.get_deployment_status")
    def test_status_starting(self, mock_get):
        mock_get.return_value = {
            "spec": {"replicas": 1},
            "status": {"readyReplicas": 0},
        }
        result = handle_status_command(make_config())
        assert "Starting" in result["content"]

    @patch("server.get_deployment_status", side_effect=Exception("fail"))
    def test_status_error(self, mock_get):
        result = handle_status_command(make_config())
        assert "Failed" in result["content"]


# --- Interaction Router Tests ---


class TestHandleInteraction:
    def test_ping_returns_pong(self):
        result = handle_interaction(make_config(), {"type": INTERACTION_PING})
        assert result == {"type": RESPONSE_PONG}

    @patch("server.handle_start_command", return_value={"content": "ok"})
    def test_routes_start(self, mock_handler):
        interaction = make_command_interaction("start")
        result = handle_interaction(make_config(), interaction)
        assert result["type"] == RESPONSE_CHANNEL_MESSAGE
        assert result["data"]["content"] == "ok"
        mock_handler.assert_called_once()

    @patch("server.handle_stop_command", return_value={"content": "ok"})
    def test_routes_stop(self, mock_handler):
        interaction = make_command_interaction("stop")
        result = handle_interaction(make_config(), interaction)
        assert result["type"] == RESPONSE_CHANNEL_MESSAGE
        mock_handler.assert_called_once()

    @patch("server.handle_status_command", return_value={"content": "ok"})
    def test_routes_status(self, mock_handler):
        interaction = make_command_interaction("status")
        result = handle_interaction(make_config(), interaction)
        assert result["type"] == RESPONSE_CHANNEL_MESSAGE
        mock_handler.assert_called_once()

    def test_unknown_command(self):
        interaction = {
            "type": INTERACTION_APPLICATION_COMMAND,
            "data": {"name": "unknown", "options": []},
        }
        result = handle_interaction(make_config(), interaction)
        assert "Unknown command" in result["data"]["content"]

    def test_unknown_subcommand(self):
        interaction = {
            "type": INTERACTION_APPLICATION_COMMAND,
            "data": {
                "name": COMMAND_NAME,
                "options": [{"name": "restart", "type": 1}],
            },
        }
        result = handle_interaction(make_config(), interaction)
        assert "Unknown subcommand" in result["data"]["content"]

    def test_no_subcommand(self):
        interaction = {
            "type": INTERACTION_APPLICATION_COMMAND,
            "data": {"name": COMMAND_NAME, "options": []},
        }
        result = handle_interaction(make_config(), interaction)
        assert "specify a subcommand" in result["data"]["content"]

    def test_unknown_interaction_type(self):
        result = handle_interaction(make_config(), {"type": 99})
        assert "Unknown interaction type" in result["data"]["content"]


# --- Notify Channel Tests ---


class TestNotifyChannel:
    def test_skips_without_credentials(self):
        config = make_config()
        # Should not raise
        notify_channel(config, "test")

    @patch("server.urlopen")
    def test_sends_message(self, mock_urlopen):
        config = make_config(discord_bot_token="tok", discord_channel_id="ch")
        notify_channel(config, "hello")
        mock_urlopen.assert_called_once()

    @patch("server.urlopen", side_effect=Exception("network error"))
    def test_handles_error(self, mock_urlopen):
        config = make_config(discord_bot_token="tok", discord_channel_id="ch")
        # Should not raise
        notify_channel(config, "hello")


# --- Command Registration Tests ---


class TestRegisterCommands:
    def test_skips_without_credentials(self, capsys):
        register_commands(make_config())
        assert "Skipping" in capsys.readouterr().out

    @patch("server.urlopen")
    def test_registers_with_credentials(self, mock_urlopen, capsys):
        mock_resp = MagicMock()
        mock_resp.status = 200
        mock_resp.__enter__ = MagicMock(return_value=mock_resp)
        mock_resp.__exit__ = MagicMock(return_value=False)
        mock_urlopen.return_value = mock_resp

        config = make_config(discord_bot_token="token", discord_app_id="appid")
        register_commands(config)
        mock_urlopen.assert_called_once()
        assert "Registered" in capsys.readouterr().out

    @patch("server.urlopen", side_effect=HTTPError("url", 400, "bad", {}, None))
    def test_handles_registration_error(self, mock_urlopen, capsys):
        config = make_config(discord_bot_token="token", discord_app_id="appid")
        register_commands(config)
        assert "Failed" in capsys.readouterr().err
