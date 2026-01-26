from __future__ import annotations

import json
import sys
import threading
import time
import urllib.request
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest.mock import patch

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import server


@dataclass
class RequestRecorder:
    requests: list[dict[str, Any]] = field(default_factory=list)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def handler(self) -> type[BaseHTTPRequestHandler]:
        recorder = self

        class RecorderHandler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(length) if length else b""
                with recorder.lock:
                    recorder.requests.append({"path": self.path, "body": body})
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b"ok")

            def log_message(self, format: str, *args: Any) -> None:
                return

        return RecorderHandler


def start_http_server(
    handler_cls: type[BaseHTTPRequestHandler],
) -> tuple[HTTPServer, threading.Thread]:
    httpd = HTTPServer(("127.0.0.1", 0), handler_cls)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    return httpd, thread


def stop_http_server(httpd: HTTPServer, thread: threading.Thread) -> None:
    httpd.shutdown()
    httpd.server_close()
    thread.join(timeout=2)


def wait_for_requests(
    recorder: RequestRecorder, count: int, timeout: float = 5.0
) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with recorder.lock:
            if len(recorder.requests) >= count:
                return
        time.sleep(0.05)
    with recorder.lock:
        raise AssertionError(f"expected {count} requests, got {len(recorder.requests)}")


def post_webhook(url: str, payload: bytes) -> None:
    req = urllib.request.Request(
        url, data=payload, headers={"Content-Type": "text/plain"}, method="POST"
    )
    with urllib.request.urlopen(req, timeout=5) as resp:
        resp.read()


def build_webhook_config(alertmanager_url: str) -> server.Config:
    return server.Config(
        alertmanager_url=alertmanager_url,
        drain_silence_alerts=[
            "LonghornVolumeStatusWarning",
            "NodeCPUHighUsage",
        ],
        drain_silence_duration="3h",
        post_reboot_silence_alerts=[
            "KubePodNotReady",
        ],
        post_reboot_silence_duration="15m",
        reboot_alert_ttl="6h",
    )


@pytest.fixture
def alertmanager_server() -> tuple[RequestRecorder, str, HTTPServer, threading.Thread]:
    recorder = RequestRecorder()
    httpd, thread = start_http_server(recorder.handler())
    url = f"http://127.0.0.1:{httpd.server_port}"
    yield recorder, url, httpd, thread
    stop_http_server(httpd, thread)


def test_webhook_drain_sends_silences_and_alert(alertmanager_server: tuple) -> None:
    recorder, url, _, _ = alertmanager_server
    previous = server.CONFIG
    server.CONFIG = build_webhook_config(url)
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}"
    try:
        post_webhook(webhook_url, b"event=drain node=borg-2")
        wait_for_requests(recorder, 3)
    finally:
        stop_http_server(webhook, webhook_thread)
        server.CONFIG = previous

    with recorder.lock:
        requests = list(recorder.requests)

    silence_requests = [
        json.loads(item["body"].decode("utf-8"))
        for item in requests
        if item["path"] == "/api/v2/silences"
    ]
    alert_requests = [
        json.loads(item["body"].decode("utf-8"))
        for item in requests
        if item["path"] == "/api/v1/alerts"
    ]

    assert len(silence_requests) == 2
    assert len(alert_requests) == 1

    silence_alerts = {item["matchers"][0]["value"] for item in silence_requests}
    assert silence_alerts == {
        "LonghornVolumeStatusWarning",
        "NodeCPUHighUsage",
    }
    for item in silence_requests:
        assert "kured drain" in item["comment"]
        assert "node=borg-2" in item["comment"]

    alert_payload = alert_requests[0][0]
    assert alert_payload["labels"]["alertname"] == "KuredNodeRebooting"
    assert alert_payload["labels"]["node"] == "borg-2"
    assert alert_payload["startsAt"] != alert_payload["endsAt"]


def test_webhook_uncordon_sends_silences_and_resolves_alert(
    alertmanager_server: tuple,
) -> None:
    recorder, url, _, _ = alertmanager_server
    previous = server.CONFIG
    server.CONFIG = build_webhook_config(url)
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}"
    try:
        post_webhook(webhook_url, b"event=uncordon node=borg-3")
        wait_for_requests(recorder, 2)
    finally:
        stop_http_server(webhook, webhook_thread)
        server.CONFIG = previous

    with recorder.lock:
        requests = list(recorder.requests)

    silence_requests = [
        json.loads(item["body"].decode("utf-8"))
        for item in requests
        if item["path"] == "/api/v2/silences"
    ]
    alert_requests = [
        json.loads(item["body"].decode("utf-8"))
        for item in requests
        if item["path"] == "/api/v1/alerts"
    ]

    assert len(silence_requests) == 1
    assert len(alert_requests) == 1

    silence_payload = silence_requests[0]
    assert silence_payload["matchers"][0]["value"] == "KubePodNotReady"
    assert "kured uncordon" in silence_payload["comment"]
    assert "node=borg-3" in silence_payload["comment"]

    alert_payload = alert_requests[0][0]
    assert alert_payload["labels"]["alertname"] == "KuredNodeRebooting"
    assert alert_payload["labels"]["node"] == "borg-3"
    assert alert_payload["startsAt"] == alert_payload["endsAt"]


@patch("server.evict_longhorn_node")
def test_webhook_drain_triggers_longhorn_eviction(
    mock_evict, alertmanager_server: tuple
) -> None:
    recorder, url, _, _ = alertmanager_server
    previous = server.CONFIG
    server.CONFIG = build_webhook_config(url)
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}"
    try:
        post_webhook(webhook_url, b"event=drain node=borg-2")
        wait_for_requests(recorder, 3)
    finally:
        stop_http_server(webhook, webhook_thread)
        server.CONFIG = previous

    # Verify Longhorn eviction was called
    mock_evict.assert_called_once_with("borg-2")


@patch("server.restore_longhorn_node")
def test_webhook_uncordon_triggers_longhorn_restore(
    mock_restore, alertmanager_server: tuple
) -> None:
    recorder, url, _, _ = alertmanager_server
    previous = server.CONFIG
    server.CONFIG = build_webhook_config(url)
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}"
    try:
        post_webhook(webhook_url, b"event=uncordon node=borg-3")
        wait_for_requests(recorder, 2)
    finally:
        stop_http_server(webhook, webhook_thread)
        server.CONFIG = previous

    # Verify Longhorn restore was called
    mock_restore.assert_called_once_with("borg-3")


@patch("server.evict_longhorn_node")
def test_webhook_drain_continues_on_longhorn_failure(
    mock_evict, alertmanager_server: tuple
) -> None:
    """Test that drain event continues even if Longhorn eviction fails."""
    recorder, url, _, _ = alertmanager_server
    previous = server.CONFIG
    server.CONFIG = build_webhook_config(url)
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}"

    # Make Longhorn eviction fail
    mock_evict.side_effect = RuntimeError("Longhorn API error")

    try:
        post_webhook(webhook_url, b"event=drain node=borg-2")
        wait_for_requests(recorder, 3)
    finally:
        stop_http_server(webhook, webhook_thread)
        server.CONFIG = previous

    # Verify silences and alerts were still sent despite Longhorn failure
    with recorder.lock:
        requests = list(recorder.requests)

    silence_requests = [item for item in requests if item["path"] == "/api/v2/silences"]
    alert_requests = [item for item in requests if item["path"] == "/api/v1/alerts"]

    assert len(silence_requests) == 2
    assert len(alert_requests) == 1


def test_webhook_health_endpoint() -> None:
    """Test that GET /health returns 200."""
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}/health"
    try:
        req = urllib.request.Request(webhook_url, method="GET")
        with urllib.request.urlopen(req, timeout=5) as resp:
            assert resp.status == 200
            body = resp.read()
            assert body == b"ok"
    finally:
        stop_http_server(webhook, webhook_thread)


def test_webhook_healthz_endpoint() -> None:
    """Test that GET /healthz returns 200."""
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}/healthz"
    try:
        req = urllib.request.Request(webhook_url, method="GET")
        with urllib.request.urlopen(req, timeout=5) as resp:
            assert resp.status == 200
            body = resp.read()
            assert body == b"ok"
    finally:
        stop_http_server(webhook, webhook_thread)


def test_webhook_root_endpoint() -> None:
    """Test that GET / returns 200."""
    webhook, webhook_thread = start_http_server(server.Handler)
    webhook_url = f"http://127.0.0.1:{webhook.server_port}/"
    try:
        req = urllib.request.Request(webhook_url, method="GET")
        with urllib.request.urlopen(req, timeout=5) as resp:
            assert resp.status == 200
            body = resp.read()
            assert body == b"ok"
    finally:
        stop_http_server(webhook, webhook_thread)
