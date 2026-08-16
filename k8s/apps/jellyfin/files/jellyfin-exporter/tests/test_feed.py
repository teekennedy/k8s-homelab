"""End-to-end WebSocket feed against the stub Jellyfin server.

Real sockets, real handshake, real framing -- so a regression in how the
exporter subscribes or interprets Jellyfin's messages shows up here
rather than in production.
"""

from __future__ import annotations

import threading
import time

import exporter
from conftest import transcoding_session


def wait_for(predicate, timeout=5.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if predicate():
            return True
        time.sleep(0.02)
    return False


def run_feed(server, api_key=None, device_id="dev", retry_seconds=10.0):
    """Start websocket_loop against a stub, and stop it on teardown."""
    state = exporter.State()
    stop = threading.Event()
    threading.Thread(
        target=exporter.websocket_loop,
        args=(
            server.url,
            server.api_key if api_key is None else api_key,
            device_id,
            state,
            stop,
            retry_seconds,
        ),
        daemon=True,
    ).start()
    return state, stop


class TestFeed:
    def test_subscribes_with_sessions_start(self, stub_jellyfin):
        state, stop = run_feed(stub_jellyfin)
        try:
            assert wait_for(lambda: stub_jellyfin.sent_message_types())
            assert stub_jellyfin.sent_message_types()[0] == "SessionsStart"
        finally:
            stop.set()

    def test_handshake_carries_credentials(self, stub_jellyfin):
        state, stop = run_feed(stub_jellyfin, device_id="dev-9")
        try:
            assert wait_for(lambda: stub_jellyfin.handshake_paths)
            path = stub_jellyfin.handshake_paths[0]
            assert f"api_key={stub_jellyfin.api_key}" in path
            assert "deviceId=dev-9" in path
        finally:
            stop.set()

    def test_session_messages_land_in_state(self, stub_jellyfin):
        state, stop = run_feed(stub_jellyfin)
        try:
            assert wait_for(lambda: state.snapshot()[1] is True)
            sessions, up, last_success = state.snapshot()
            text = exporter.render(sessions, up=up, last_success=last_success)
            assert "jellyfin_active_streams 1" in text
            assert "jellyfin_active_video_transcodes 1" in text
        finally:
            stop.set()

    def test_later_pushes_replace_earlier_state(self, stub_jellyfin):
        state, stop = run_feed(stub_jellyfin)
        try:
            assert wait_for(lambda: len(state.snapshot()[0]) == 1)
            stub_jellyfin.push({"MessageType": "Sessions", "Data": []})
            assert wait_for(lambda: state.snapshot()[0] == [])
        finally:
            stop.set()

    def test_force_keep_alive_is_answered(self, stub_jellyfin):
        """Jellyfin drops clients that ignore ForceKeepAlive."""
        stub_jellyfin.to_send = [{"MessageType": "ForceKeepAlive", "Data": 60}]
        state, stop = run_feed(stub_jellyfin)
        try:
            assert wait_for(lambda: "KeepAlive" in stub_jellyfin.sent_message_types())
        finally:
            stop.set()

    def test_unrelated_message_types_are_ignored(self, stub_jellyfin):
        stub_jellyfin.to_send = [
            {"MessageType": "ScheduledTasksInfo", "Data": []},
            {"MessageType": "Sessions", "Data": [transcoding_session()]},
        ]
        state, stop = run_feed(stub_jellyfin)
        try:
            assert wait_for(lambda: state.snapshot()[1] is True)
            assert len(state.snapshot()[0]) == 1
        finally:
            stop.set()

    def test_a_rejected_api_key_leaves_state_stale_and_keeps_retrying(
        self, stub_jellyfin
    ):
        """A bad key must not look like "nobody is watching".

        It must also not kill the reconnect loop: the key can be fixed
        without restarting the exporter, but only if it is still trying.
        """
        state, stop = run_feed(stub_jellyfin, api_key="wrong", retry_seconds=0.05)
        try:
            assert wait_for(lambda: len(stub_jellyfin.rejected_handshakes) >= 2)
            assert state.snapshot()[1] is False
            assert stub_jellyfin.sent_message_types() == []
        finally:
            stop.set()

    def test_the_feed_reconnects_after_the_server_hangs_up(self, stub_jellyfin):
        state, stop = run_feed(stub_jellyfin, retry_seconds=0.05)
        try:
            assert wait_for(lambda: len(stub_jellyfin.handshake_paths) >= 1)
            stub_jellyfin.disconnect_all()
            assert wait_for(lambda: len(stub_jellyfin.handshake_paths) >= 2)
        finally:
            stop.set()
