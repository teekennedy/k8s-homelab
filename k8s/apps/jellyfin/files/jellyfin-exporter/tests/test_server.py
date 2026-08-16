"""HTTP surface: the /metrics endpoint and the REST reconcile."""

from __future__ import annotations

import threading
import urllib.request

import pytest

import exporter
from conftest import FakeJellyfin


class TestRestReconcile:
    def test_fetches_sessions(self):
        with FakeJellyfin(sessions=[{"Id": "a"}, {"Id": "b"}]) as jf:
            client = exporter.JellyfinClient(jf.url, jf.api_key)
            assert len(client.get_sessions()) == 2

    def test_sends_the_api_key_as_a_token_header(self, stub_jellyfin):
        """Query-string keys end up in access logs; use the header instead."""
        exporter.JellyfinClient(stub_jellyfin.url, stub_jellyfin.api_key).get_sessions()
        _, headers = stub_jellyfin.rest_requests[0]
        assert headers.get("X-Emby-Token") == stub_jellyfin.api_key

    def test_requests_the_sessions_endpoint(self, stub_jellyfin):
        exporter.JellyfinClient(stub_jellyfin.url, stub_jellyfin.api_key).get_sessions()
        assert stub_jellyfin.rest_requests[0][0].startswith("/Sessions")

    def test_trailing_slash_on_base_url_is_tolerated(self, stub_jellyfin):
        client = exporter.JellyfinClient(stub_jellyfin.url + "/", stub_jellyfin.api_key)
        client.get_sessions()
        assert stub_jellyfin.rest_requests[0][0].startswith("/Sessions")
        assert "//Sessions" not in stub_jellyfin.rest_requests[0][0]

    def test_http_error_propagates(self, stub_jellyfin):
        with pytest.raises(Exception):
            exporter.JellyfinClient(stub_jellyfin.url, "BAD").get_sessions()

    def test_a_server_side_failure_propagates(self):
        with FakeJellyfin(rest_status=500) as jf:
            with pytest.raises(Exception):
                exporter.JellyfinClient(jf.url, jf.api_key).get_sessions()


class TestMetricsEndpoint:
    @pytest.fixture
    def served(self):
        state = exporter.State(stale_after=90)
        server = exporter.build_metrics_server("127.0.0.1", 0, state)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        url = f"http://127.0.0.1:{server.server_port}"
        yield state, url
        server.shutdown()
        server.server_close()

    def test_metrics_endpoint_serves_exposition_text(self, served):
        state, url = served
        state.update([])
        with urllib.request.urlopen(f"{url}/metrics") as resp:
            body = resp.read().decode()
            assert resp.status == 200
            assert "text/plain" in resp.headers["Content-Type"]
        assert "jellyfin_active_streams 0" in body

    def test_metrics_reflect_current_state(self, served):
        state, url = served
        state.update(
            [
                {
                    "Id": "s",
                    "UserName": "alice",
                    "Client": "Chromecast",
                    "DeviceName": "Living Room TV",
                    "NowPlayingItem": {"Id": "i"},
                    "PlayState": {"IsPaused": False, "PlayMethod": "Transcode"},
                    "TranscodingInfo": {
                        "IsVideoDirect": False,
                        "IsAudioDirect": False,
                    },
                }
            ]
        )
        with urllib.request.urlopen(f"{url}/metrics") as resp:
            body = resp.read().decode()
        assert "jellyfin_active_streams 1" in body
        assert "jellyfin_active_video_transcodes 1" in body

    def test_healthz_is_always_ok_so_liveness_survives_jellyfin_outages(self, served):
        """Restarting the exporter cannot fix a Jellyfin outage."""
        _, url = served
        with urllib.request.urlopen(f"{url}/healthz") as resp:
            assert resp.status == 200

    def test_unknown_path_returns_404(self, served):
        _, url = served
        with pytest.raises(urllib.error.HTTPError) as excinfo:
            urllib.request.urlopen(f"{url}/nope")
        assert excinfo.value.code == 404
