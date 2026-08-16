"""Integration tests: the exporter against a whole Jellyfin server.

These run by default against the stub server in conftest.py, so CI catches
a broken REST call or handshake without anybody port-forwarding anything.

To run the same tests against a real server:

    kubectl -n jellyfin port-forward svc/jellyfin-main 8096:8096

    export JELLYFIN_URL=http://localhost:8096
    export JELLYFIN_API_KEY=<key from Dashboard -> API Keys>
    uv run pytest -m integration

They are read-only: they query sessions and open a WebSocket, and never
start, stop, or modify playback, so they are safe to run while people are
watching.
"""

from __future__ import annotations

import threading

import pytest

import exporter

pytestmark = pytest.mark.integration


@pytest.fixture
def client(jellyfin_target):
    return exporter.JellyfinClient(*jellyfin_target)


class TestRest:
    def test_sessions_endpoint_authenticates_and_returns_a_list(self, client):
        assert isinstance(client.get_sessions(), list)

    def test_every_session_classifies_without_error(self, client):
        for raw in client.get_sessions():
            session = exporter.classify(raw)
            assert isinstance(session.playing, bool)
            assert session.user

    def test_render_produces_valid_exposition_for_live_data(self, client):
        text = exporter.render(client.get_sessions(), up=True, last_success=1.0)
        for line in text.splitlines():
            if line.startswith("#") or not line:
                continue
            name, _, value = line.rpartition(" ")
            assert name, f"unparseable line: {line!r}"
            float(value)

    def test_transcode_fields_are_present(self, client, jellyfin_is_live):
        """The contract the whole exporter rests on.

        IsVideoDirect is what separates a free audio-only transcode from
        real GPU work. The stub always serves a transcoding session; a live
        server only sometimes has one.
        """
        transcoding = [s for s in client.get_sessions() if s.get("TranscodingInfo")]
        if not transcoding:
            if jellyfin_is_live:
                pytest.skip("no transcode in flight on the live server")
            pytest.fail("the stub server should always serve a transcoding session")
        for raw in transcoding:
            info = raw["TranscodingInfo"]
            assert "IsVideoDirect" in info
            assert "IsAudioDirect" in info

    def test_a_bad_api_key_is_rejected(self, jellyfin_target):
        url, _ = jellyfin_target
        with pytest.raises(Exception):
            exporter.JellyfinClient(url, "not-a-real-key").get_sessions()


class TestWebSocket:
    def test_handshake_succeeds(self, jellyfin_target):
        url, api_key = jellyfin_target
        with exporter.connect_socket(url, api_key, "jellyfin-exporter-test"):
            pass

    def test_receives_a_sessions_message(self, jellyfin_target):
        """Jellyfin pushes the first Sessions payload right after subscribing."""
        url, api_key = jellyfin_target
        state = exporter.State()
        stop = threading.Event()
        threading.Thread(
            target=exporter.websocket_loop,
            args=(url, api_key, "jellyfin-exporter-test", state, stop),
            daemon=True,
        ).start()
        try:
            # Poll for freshness rather than sleeping a fixed interval.
            tick = threading.Event()
            for _ in range(100):
                if state.snapshot()[1]:
                    break
                tick.wait(0.1)
            sessions, up, _ = state.snapshot()
            assert up, "no Sessions message arrived within 10s"
            assert isinstance(sessions, list)
        finally:
            stop.set()

    def test_bad_api_key_is_rejected(self, jellyfin_target):
        url, _ = jellyfin_target
        with pytest.raises(Exception):
            with exporter.connect_socket(url, "not-a-real-key", "test"):
                pass
