import json
import socket
import sys
import urllib.error
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import MagicMock, Mock, mock_open, patch

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import server


def test_parse_duration_seconds():
    assert server.parse_duration("30s") == timedelta(seconds=30)


def test_parse_duration_minutes():
    assert server.parse_duration("15m") == timedelta(minutes=15)


def test_parse_duration_hours():
    assert server.parse_duration("3h") == timedelta(hours=3)


def test_parse_duration_invalid_unit():
    with pytest.raises(ValueError):
        server.parse_duration("5d")


def test_isoformat_utc_suffix():
    ts = datetime(2026, 1, 20, 7, 3, 0, tzinfo=timezone.utc)
    assert server.isoformat(ts).endswith("Z")


def test_parse_message_plain():
    event, node, raw = server.parse_message(b"event=drain node=borg-2")
    assert event == "drain"
    assert node == "borg-2"
    assert "event=drain" in raw


def test_parse_message_json():
    body = b'{"message": "event=uncordon node=borg-3"}'
    event, node, raw = server.parse_message(body)
    assert event == "uncordon"
    assert node == "borg-3"
    assert "event=uncordon" in raw


def test_build_silence_payload_timestamps():
    now = datetime(2026, 1, 20, 7, 0, 0, tzinfo=timezone.utc)
    payload = server.build_silence_payload(
        "ExampleAlert",
        timedelta(minutes=15),
        "borg-1",
        "testing",
        now=now,
    )
    assert payload["matchers"][0]["value"] == "ExampleAlert"
    assert payload["startsAt"].endswith("Z")
    assert payload["endsAt"].endswith("Z")


@patch.dict(
    "os.environ",
    {"KUBERNETES_SERVICE_HOST": "kubernetes.test", "KUBERNETES_SERVICE_PORT": "443"},
)
@patch("ssl.create_default_context")
@patch("pathlib.Path.read_text")
def test_get_k8s_auth(mock_read_text, mock_ssl_context):
    mock_read_text.return_value = "test-token-123"
    mock_ssl = MagicMock()
    mock_ssl_context.return_value = mock_ssl

    token, api_url, ssl_context = server.get_k8s_auth()

    assert token == "test-token-123"
    assert api_url == "https://kubernetes.test:443"
    assert ssl_context == mock_ssl
    mock_ssl_context.assert_called_once()


@patch("server.get_k8s_auth")
@patch("urllib.request.urlopen")
def test_k8s_request_get(mock_urlopen, mock_auth):
    mock_auth.return_value = ("test-token", "https://k8s.test", MagicMock())
    mock_response = Mock()
    mock_response.read.return_value = b'{"status": "ok"}'
    mock_urlopen.return_value.__enter__.return_value = mock_response

    result = server.k8s_request("GET", "/api/v1/nodes/test-node")

    assert result == {"status": "ok"}
    assert mock_urlopen.called


@patch("server.get_k8s_auth")
@patch("urllib.request.urlopen")
def test_k8s_request_put_with_body(mock_urlopen, mock_auth):
    mock_auth.return_value = ("test-token", "https://k8s.test", MagicMock())
    mock_response = Mock()
    mock_response.read.return_value = b'{"updated": true}'
    mock_urlopen.return_value.__enter__.return_value = mock_response

    body = {"spec": {"allowScheduling": False}}
    result = server.k8s_request("PUT", "/api/v1/nodes/test-node", body)

    assert result == {"updated": True}
    assert mock_urlopen.called


@patch("server.get_k8s_auth")
@patch("urllib.request.urlopen")
def test_k8s_request_handles_http_error(mock_urlopen, mock_auth):
    mock_auth.return_value = ("test-token", "https://k8s.test", MagicMock())
    error = urllib.error.HTTPError("url", 404, "Not Found", {}, None)
    error.read = lambda: b'{"error": "not found"}'
    mock_urlopen.side_effect = error

    with pytest.raises(RuntimeError, match="K8s API error: 404"):
        server.k8s_request("GET", "/api/v1/nodes/missing")


@patch("socket.getaddrinfo")
def test_resolve_alertmanager_bases_dedupes_and_sorts(mock_getaddrinfo):
    mock_getaddrinfo.return_value = [
        (None, None, None, None, ("10.0.0.2", 9093)),
        (None, None, None, None, ("10.0.0.1", 9093)),
        (None, None, None, None, ("10.0.0.1", 9093)),
    ]

    bases = server.resolve_alertmanager_bases("http://alertmanager-operated:9093")

    assert bases == ["http://10.0.0.1:9093", "http://10.0.0.2:9093"]
    mock_getaddrinfo.assert_called_once_with(
        "alertmanager-operated", 9093, type=socket.SOCK_STREAM
    )


@patch("urllib.request.urlopen")
def test_broadcast_json_posts_to_every_replica(mock_urlopen):
    mock_urlopen.return_value.__enter__.return_value = Mock(read=lambda: b"")
    bases = ["http://10.0.0.1:9093", "http://10.0.0.2:9093", "http://10.0.0.3:9093"]

    server.broadcast_json("/api/v2/alerts", [{"labels": {"alertname": "x"}}], bases)

    posted_urls = [call.args[0].full_url for call in mock_urlopen.call_args_list]
    assert posted_urls == [
        "http://10.0.0.1:9093/api/v2/alerts",
        "http://10.0.0.2:9093/api/v2/alerts",
        "http://10.0.0.3:9093/api/v2/alerts",
    ]


@patch("urllib.request.urlopen")
def test_broadcast_json_raises_on_partial_failure(mock_urlopen):
    ok_response = MagicMock()
    ok_response.__enter__.return_value = Mock(read=lambda: b"")
    mock_urlopen.side_effect = [OSError("unreachable"), ok_response]
    bases = ["http://10.0.0.1:9093", "http://10.0.0.2:9093"]

    with pytest.raises(OSError, match="unreachable"):
        server.broadcast_json("/api/v2/alerts", [{"labels": {"alertname": "x"}}], bases)

    # Every replica is still attempted, even after an earlier one fails.
    assert mock_urlopen.call_count == 2


@patch("urllib.request.urlopen")
def test_broadcast_json_raises_if_all_replicas_fail(mock_urlopen):
    mock_urlopen.side_effect = OSError("unreachable")
    bases = ["http://10.0.0.1:9093", "http://10.0.0.2:9093"]

    with pytest.raises(OSError, match="unreachable"):
        server.broadcast_json("/api/v2/alerts", [{"labels": {"alertname": "x"}}], bases)
