"""How the exporter addresses and subscribes to Jellyfin's /socket.

RFC 6455 framing belongs to the `websockets` library, so there is nothing
to test there. What is ours is the URL we dial and the shape of the
subscription message, both of which Jellyfin is picky about.
"""

from __future__ import annotations

import exporter


class TestSocketUrl:
    def test_socket_url_carries_api_key_and_device_id(self):
        url = exporter.socket_url("http://jf:8096", "SECRET", "dev-1")
        assert url.startswith("ws://jf:8096/socket?")
        assert "api_key=SECRET" in url
        assert "deviceId=dev-1" in url

    def test_https_maps_to_wss(self):
        assert exporter.socket_url("https://jf", "k", "d").startswith(
            "wss://jf/socket?"
        )

    def test_special_characters_in_the_key_are_encoded(self):
        url = exporter.socket_url("http://jf", "a+b/c=", "d")
        assert "api_key=a%2Bb%2Fc%3D" in url


class TestSubscriptionMessage:
    def test_sessions_start_uses_duetime_comma_period(self):
        """BasePeriodicWebSocketListener parses Data as "<dueMs>,<periodMs>"."""
        msg = exporter.sessions_start_message(due_ms=0, period_ms=1500)
        assert msg["MessageType"] == "SessionsStart"
        assert msg["Data"] == "0,1500"
