"""A missing API key must degrade, not crash-loop.

Exiting on a missing secret would give a CrashLoopBackOff whose only
symptom is an absent metric, which reads exactly like "Jellyfin is
unreachable" -- and both let node reboots proceed, so the reboot gate
quietly stops gating.

So a missing or wrong API key degrades to `jellyfin_up 0` and keeps
serving. JellyfinExporterDown is what tells you to go fix it.
"""

from __future__ import annotations

import threading
import urllib.request

import pytest

import exporter


@pytest.fixture(autouse=True)
def clean_env(monkeypatch):
    for name in ("JELLYFIN_URL", "JELLYFIN_API_KEY", "LISTEN_PORT", "LISTEN_HOST"):
        monkeypatch.delenv(name, raising=False)


class TestCollectorStartup:
    def test_collectors_do_not_start_without_an_api_key(self):
        state = exporter.State()
        stop = threading.Event()
        started = exporter.start_collectors(
            base_url="http://jellyfin:8096",
            api_key="",
            device_id="test",
            state=state,
            stop=stop,
        )
        assert started is False
        stop.set()

    def test_collectors_do_not_start_without_a_url(self):
        state = exporter.State()
        stop = threading.Event()
        assert (
            exporter.start_collectors(
                base_url="",
                api_key="key",
                device_id="test",
                state=state,
                stop=stop,
            )
            is False
        )
        stop.set()

    def test_state_stays_down_when_collectors_never_start(self):
        state = exporter.State()
        stop = threading.Event()
        exporter.start_collectors(
            base_url="", api_key="", device_id="t", state=state, stop=stop
        )
        sessions, up, last_success = state.snapshot()
        text = exporter.render(sessions, up=up, last_success=last_success)
        assert "jellyfin_up 0" in text
        assert "jellyfin_active_streams 0" in text
        stop.set()


class TestMain:
    def test_main_serves_metrics_even_with_no_credentials(self, monkeypatch):
        """The exact CreateContainerConfigError / CrashLoopBackOff scenario:
        secret missing, so the env var is empty."""
        monkeypatch.setenv("LISTEN_HOST", "127.0.0.1")
        monkeypatch.setenv("LISTEN_PORT", "0")

        captured = {}

        def fake_serve(server):
            captured["port"] = server.server_port
            threading.Thread(target=server.serve_forever, daemon=True).start()
            url = f"http://127.0.0.1:{server.server_port}"
            with urllib.request.urlopen(f"{url}/metrics") as resp:
                captured["metrics"] = resp.read().decode()
            with urllib.request.urlopen(f"{url}/healthz") as resp:
                captured["health"] = resp.status
            server.shutdown()

        assert exporter.main(serve=fake_serve) == 0
        assert captured["health"] == 200
        assert "jellyfin_up 0" in captured["metrics"]
        assert "jellyfin_active_streams 0" in captured["metrics"]

    def test_main_does_not_exit_nonzero_on_missing_credentials(self, monkeypatch):
        """A nonzero exit restarts the container, which is the crash loop."""
        monkeypatch.setenv("LISTEN_HOST", "127.0.0.1")
        monkeypatch.setenv("LISTEN_PORT", "0")
        assert exporter.main(serve=lambda server: server.server_close()) == 0
