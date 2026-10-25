"""Library item counts (jellyfin_library_items).

These feed dashboards only. They must never touch `jellyfin_up` or the
stream gauges, which back the kured reboot gate.
"""

from __future__ import annotations

import threading

import exporter
from conftest import ITEM_COUNTS, FakeJellyfin


def series(text: str) -> dict[str, float]:
    out = {}
    for line in text.splitlines():
        if line and not line.startswith("#"):
            name, _, value = line.rpartition(" ")
            out[name] = float(value)
    return out


class TestLibraryCounts:
    def test_maps_known_keys_to_type_labels(self):
        counts = exporter.library_counts(ITEM_COUNTS)
        assert counts["movie"] == 298
        assert counts["series"] == 138
        assert counts["episode"] == 6583
        assert counts["box_set"] == 1

    def test_drops_item_count(self):
        assert "item" not in exporter.library_counts(ITEM_COUNTS)
        assert len(exporter.library_counts(ITEM_COUNTS)) == 11

    def test_skips_missing_and_non_integer_values(self):
        counts = exporter.library_counts(
            {"MovieCount": 3, "SeriesCount": None, "EpisodeCount": True}
        )
        assert counts == {"movie": 3}


class TestRender:
    def test_emits_one_series_per_type(self):
        text = exporter.render(
            [], up=True, last_success=1.0, library={"movie": 298, "series": 138}
        )
        m = series(text)
        assert m['jellyfin_library_items{type="movie"}'] == 298
        assert m['jellyfin_library_items{type="series"}'] == 138
        assert text.count("# TYPE jellyfin_library_items gauge") == 1

    def test_omitted_when_unknown(self):
        text = exporter.render([], up=True, last_success=1.0, library=None)
        assert "jellyfin_library_items" not in text

    def test_independent_of_session_health(self):
        """A failing session query zeroes the stream gauges but must not
        hide library counts that are still fresh."""
        m = series(
            exporter.render([], up=False, last_success=0.0, library={"movie": 5})
        )
        assert m["jellyfin_up"] == 0
        assert m['jellyfin_library_items{type="movie"}'] == 5


class TestExpiringCounts:
    def test_none_before_first_update(self):
        assert exporter.ExpiringCounts().snapshot() is None

    def test_fresh_then_stale(self):
        now = [1000.0]
        library = exporter.ExpiringCounts(stale_after=60, clock=lambda: now[0])
        library.update({"movie": 1})
        assert library.snapshot() == {"movie": 1}
        now[0] += 61
        assert library.snapshot() is None


class TestCollection:
    def test_start_collectors_seeds_library_from_server(self):
        with FakeJellyfin() as server:
            state = exporter.State()
            library = exporter.ExpiringCounts()
            stop = threading.Event()
            try:
                assert exporter.start_collectors(
                    server.url, server.api_key, "t", state, stop, library=library
                )
                assert library.snapshot()["episode"] == 6583
            finally:
                stop.set()
            paths = [path for path, _ in server.rest_requests]
            assert "/Items/Counts" in paths

    def test_failed_query_keeps_last_counts(self):
        with FakeJellyfin() as server:
            client = exporter.JellyfinClient(server.url, server.api_key)
            library = exporter.ExpiringCounts()
            exporter.refresh_library(client, library)
            server.rest_status = 500
            exporter.refresh_library(client, library)
            assert library.snapshot()["movie"] == 298
