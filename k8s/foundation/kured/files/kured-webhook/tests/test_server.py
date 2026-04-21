import json
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
