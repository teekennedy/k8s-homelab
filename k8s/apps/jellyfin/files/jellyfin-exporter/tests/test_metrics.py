"""Prometheus text rendering.

`jellyfin_active_streams` is consumed by the JellyfinStreamActive alert
that gates kured reboots, so its paused-exclusion semantics are load
bearing: a paused stream must not hold the gate open, or an AFK viewer
blocks node patching indefinitely.
"""

from __future__ import annotations

import exporter


def parse(text: str) -> dict[str, float]:
    """Flatten exposition text into {series: value}, ignoring HELP/TYPE."""
    out = {}
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        name, _, value = line.rpartition(" ")
        out[name] = float(value)
    return out


def sess(**kw):
    base = {
        "Id": "s",
        "UserName": "alice",
        "Client": "Jellyfin Web",
        "DeviceName": "Firefox",
        "NowPlayingItem": {"Id": "i", "Name": "Movie"},
        "PlayState": {"IsPaused": False, "PlayMethod": "DirectPlay"},
    }
    base.update(kw)
    return base


def transcoding(video_direct: bool, paused: bool = False, **kw):
    return sess(
        PlayState={"IsPaused": paused, "PlayMethod": "Transcode"},
        TranscodingInfo={
            "IsVideoDirect": video_direct,
            "IsAudioDirect": False,
            **kw,
        },
    )


class TestAggregateGauges:
    def test_empty_server_reports_zeroes(self):
        m = parse(exporter.render([], up=True, last_success=100.0))
        assert m["jellyfin_active_streams"] == 0
        assert m["jellyfin_paused_streams"] == 0
        assert m["jellyfin_active_video_transcodes"] == 0
        assert m["jellyfin_active_audio_transcodes"] == 0

    def test_gauges_are_always_present_so_alerts_never_see_absent(self):
        """absent() would make the kured gate fail in whichever direction
        the rule was not written for. Always emit the series."""
        text = exporter.render([], up=True, last_success=100.0)
        for name in (
            "jellyfin_active_streams",
            "jellyfin_paused_streams",
            "jellyfin_active_video_transcodes",
            "jellyfin_active_audio_transcodes",
        ):
            assert f"{name} " in text

    def test_idle_sessions_are_not_counted_as_streams(self):
        m = parse(
            exporter.render([sess(NowPlayingItem=None)], up=True, last_success=1.0)
        )
        assert m["jellyfin_active_streams"] == 0

    def test_paused_stream_counts_as_paused_not_active(self):
        m = parse(
            exporter.render(
                [sess(PlayState={"IsPaused": True, "PlayMethod": "DirectPlay"})],
                up=True,
                last_success=1.0,
            )
        )
        assert m["jellyfin_active_streams"] == 0
        assert m["jellyfin_paused_streams"] == 1

    def test_video_and_audio_transcodes_are_counted_separately(self):
        sessions = [
            transcoding(video_direct=True),  # audio only, free
            transcoding(video_direct=True),  # audio only, free
            transcoding(video_direct=False),  # real GPU work
        ]
        m = parse(exporter.render(sessions, up=True, last_success=1.0))
        assert m["jellyfin_active_streams"] == 3
        assert m["jellyfin_active_audio_transcodes"] == 3
        assert m["jellyfin_active_video_transcodes"] == 1

    def test_paused_transcodes_are_excluded_from_active_counts(self):
        m = parse(
            exporter.render(
                [transcoding(video_direct=False, paused=True)],
                up=True,
                last_success=1.0,
            )
        )
        assert m["jellyfin_active_video_transcodes"] == 0
        assert m["jellyfin_paused_streams"] == 1


class TestHealthMetrics:
    def test_up_is_reported(self):
        assert parse(exporter.render([], up=True, last_success=5.0))["jellyfin_up"] == 1
        assert (
            parse(exporter.render([], up=False, last_success=5.0))["jellyfin_up"] == 0
        )

    def test_last_success_timestamp_is_reported(self):
        m = parse(exporter.render([], up=True, last_success=1234.5))
        assert m["jellyfin_exporter_last_success_timestamp_seconds"] == 1234.5

    def test_counts_are_zero_when_scraping_jellyfin_is_failing(self):
        """Stale session state must not keep the reboot gate shut forever."""
        m = parse(exporter.render([sess()], up=False, last_success=1.0))
        assert m["jellyfin_active_streams"] == 0


class TestSessionInfo:
    def test_info_series_carries_identity_and_method_labels(self):
        text = exporter.render(
            [
                transcoding(
                    video_direct=False,
                    HardwareAccelerationType="qsv",
                    TranscodeReasons=["VideoResolutionNotSupported"],
                )
            ],
            up=True,
            last_success=1.0,
        )
        assert 'user="alice"' in text
        assert 'client="Jellyfin Web"' in text
        assert 'device="Firefox"' in text
        assert 'play_method="Transcode"' in text
        assert 'video_transcode="true"' in text
        assert 'hardware_acceleration="qsv"' in text
        assert 'transcode_reasons="VideoResolutionNotSupported"' in text

    def test_info_series_omits_item_name_to_bound_cardinality(self):
        text = exporter.render([sess()], up=True, last_success=1.0)
        assert "Movie" not in text

    def test_label_values_are_escaped(self):
        text = exporter.render(
            [sess(DeviceName='Bob\\"s "TV"')], up=True, last_success=1.0
        )
        assert 'device="Bob\\\\\\"s \\"TV\\""' in text

    def test_no_info_series_when_nothing_is_playing(self):
        text = exporter.render([sess(NowPlayingItem=None)], up=True, last_success=1.0)
        assert "jellyfin_session_info" not in text
