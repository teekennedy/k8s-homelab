"""Session classification.

The whole point of this exporter is that Jellyfin's own UI (and the
Playback Reporting plugin) both label an audio-only transcode as
"Transcode", which makes a server look far busier than it is. A
1080p H264 video passed through untouched with only EAC3 -> AAC audio
costs no GPU at all, while a real video encode does. These tests pin
that distinction down.
"""

from __future__ import annotations

import exporter


def session(**overrides):
    """A minimal SessionInfoDto as Jellyfin serialises it."""
    base = {
        "Id": "sess1",
        "UserName": "alice",
        "Client": "Jellyfin Web",
        "DeviceName": "Firefox",
        "NowPlayingItem": {"Id": "item1", "Name": "Some Movie"},
        "PlayState": {"IsPaused": False, "PlayMethod": "DirectPlay"},
    }
    base.update(overrides)
    return base


class TestPlaybackState:
    def test_session_with_no_now_playing_item_is_not_playing(self):
        s = exporter.classify(session(NowPlayingItem=None))
        assert s.playing is False

    def test_session_with_now_playing_item_is_playing(self):
        assert exporter.classify(session()).playing is True

    def test_paused_is_read_from_playstate(self):
        s = exporter.classify(
            session(PlayState={"IsPaused": True, "PlayMethod": "DirectPlay"})
        )
        assert s.paused is True
        assert s.playing is True

    def test_missing_playstate_is_treated_as_not_paused(self):
        assert exporter.classify(session(PlayState={})).paused is False


class TestTranscodeClassification:
    def test_direct_play_has_no_transcode(self):
        s = exporter.classify(session())
        assert s.play_method == "DirectPlay"
        assert s.video_transcode is False
        assert s.audio_transcode is False

    def test_direct_stream_has_no_transcode(self):
        s = exporter.classify(
            session(PlayState={"IsPaused": False, "PlayMethod": "DirectStream"})
        )
        assert s.play_method == "DirectStream"
        assert s.video_transcode is False
        assert s.audio_transcode is False

    def test_audio_only_transcode_is_not_a_video_transcode(self):
        """v:direct a:aac -- the case that inflates the transcode count."""
        s = exporter.classify(
            session(
                PlayState={"IsPaused": False, "PlayMethod": "Transcode"},
                TranscodingInfo={
                    "IsVideoDirect": True,
                    "IsAudioDirect": False,
                    "VideoCodec": "h264",
                    "AudioCodec": "aac",
                },
            )
        )
        assert s.video_transcode is False
        assert s.audio_transcode is True

    def test_video_transcode_is_flagged(self):
        s = exporter.classify(
            session(
                PlayState={"IsPaused": False, "PlayMethod": "Transcode"},
                TranscodingInfo={
                    "IsVideoDirect": False,
                    "IsAudioDirect": False,
                    "VideoCodec": "h264",
                    "AudioCodec": "aac",
                    "HardwareAccelerationType": "qsv",
                    "TranscodeReasons": ["VideoResolutionNotSupported"],
                },
            )
        )
        assert s.video_transcode is True
        assert s.audio_transcode is True
        assert s.hardware_acceleration == "qsv"
        assert s.transcode_reasons == "VideoResolutionNotSupported"

    def test_transcode_reasons_may_arrive_as_a_comma_string(self):
        """Older servers send a flags string rather than a list."""
        s = exporter.classify(
            session(
                PlayState={"IsPaused": False, "PlayMethod": "Transcode"},
                TranscodingInfo={
                    "IsVideoDirect": False,
                    "IsAudioDirect": False,
                    "TranscodeReasons": "VideoCodecNotSupported, AudioCodecNotSupported",
                },
            )
        )
        assert s.transcode_reasons == "AudioCodecNotSupported,VideoCodecNotSupported"

    def test_missing_transcoding_info_with_transcode_playmethod(self):
        """Jellyfin briefly reports PlayMethod=Transcode before TranscodingInfo
        is populated. Assume no video transcode rather than inventing GPU load."""
        s = exporter.classify(
            session(PlayState={"IsPaused": False, "PlayMethod": "Transcode"})
        )
        assert s.video_transcode is False
        assert s.audio_transcode is False


class TestIdentityLabels:
    def test_labels_are_taken_from_the_session(self):
        s = exporter.classify(
            session(UserName="bob", Client="Chromecast", DeviceName="Living Room TV")
        )
        assert s.user == "bob"
        assert s.client == "Chromecast"
        assert s.device == "Living Room TV"

    def test_missing_identity_fields_fall_back_to_unknown(self):
        s = exporter.classify(session(UserName=None, Client=None, DeviceName=None))
        assert s.user == "unknown"
        assert s.client == "unknown"
        assert s.device == "unknown"
