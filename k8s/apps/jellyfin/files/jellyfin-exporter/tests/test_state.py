"""Freshness tracking.

The WebSocket can drop silently: the socket stays open while the server
stops pushing. If that happened and we kept serving the last known
sessions, a stream that ended hours ago would hold the kured reboot gate
shut forever. State therefore expires on age, not on connection status.
"""

from __future__ import annotations

import exporter


class FakeClock:
    def __init__(self, now: float = 1000.0):
        self.now = now

    def __call__(self) -> float:
        return self.now

    def advance(self, seconds: float) -> None:
        self.now += seconds


def sess(paused: bool = False):
    return {
        "Id": "s",
        "UserName": "alice",
        "Client": "Jellyfin Web",
        "DeviceName": "Firefox",
        "NowPlayingItem": {"Id": "i"},
        "PlayState": {"IsPaused": paused, "PlayMethod": "DirectPlay"},
    }


class TestFreshness:
    def test_starts_down_before_any_successful_update(self):
        state = exporter.State(stale_after=90, clock=FakeClock())
        _, up, _ = state.snapshot()
        assert up is False

    def test_update_marks_it_up(self):
        state = exporter.State(stale_after=90, clock=FakeClock())
        state.update([sess()])
        sessions, up, _ = state.snapshot()
        assert up is True
        assert len(sessions) == 1

    def test_records_the_time_of_the_last_success(self):
        clock = FakeClock(now=500.0)
        state = exporter.State(stale_after=90, clock=clock)
        state.update([])
        _, _, last_success = state.snapshot()
        assert last_success == 500.0

    def test_goes_down_once_updates_stop_arriving(self):
        clock = FakeClock()
        state = exporter.State(stale_after=90, clock=clock)
        state.update([sess()])

        clock.advance(89)
        assert state.snapshot()[1] is True

        clock.advance(2)
        assert state.snapshot()[1] is False

    def test_a_fresh_update_revives_it(self):
        clock = FakeClock()
        state = exporter.State(stale_after=90, clock=clock)
        state.update([sess()])
        clock.advance(200)
        assert state.snapshot()[1] is False

        state.update([sess()])
        assert state.snapshot()[1] is True

    def test_stale_state_renders_zero_active_streams(self):
        """The gate must open when we can no longer tell what is playing."""
        clock = FakeClock()
        state = exporter.State(stale_after=90, clock=clock)
        state.update([sess()])
        clock.advance(500)

        sessions, up, last_success = state.snapshot()
        text = exporter.render(sessions, up=up, last_success=last_success)
        assert "jellyfin_active_streams 0" in text
        assert "jellyfin_up 0" in text


class TestUpdates:
    def test_an_empty_update_is_still_a_success(self):
        """Nobody watching is information, not a failure."""
        state = exporter.State(stale_after=90, clock=FakeClock())
        state.update([])
        sessions, up, _ = state.snapshot()
        assert up is True
        assert sessions == []

    def test_later_updates_replace_earlier_ones(self):
        state = exporter.State(stale_after=90, clock=FakeClock())
        state.update([sess(), sess()])
        state.update([sess()])
        assert len(state.snapshot()[0]) == 1

    def test_snapshot_does_not_alias_internal_state(self):
        state = exporter.State(stale_after=90, clock=FakeClock())
        state.update([sess()])
        sessions, _, _ = state.snapshot()
        sessions.append(sess())
        assert len(state.snapshot()[0]) == 1
