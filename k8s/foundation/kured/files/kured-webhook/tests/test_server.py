import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

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
