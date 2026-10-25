"""Media directory size (jellyfin_media_size_bytes)."""

from __future__ import annotations

import threading

import pytest

import exporter


def write(path, size: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"x" * size)


@pytest.fixture
def media(tmp_path):
    write(tmp_path / "movies" / "A (2001)" / "a.mkv", 100)
    write(tmp_path / "movies" / "B (2002)" / "b.mkv", 50)
    write(tmp_path / "shows" / "C" / "Season 01" / "c.mkv", 7)
    write(tmp_path / "stray.nfo", 3)
    return tmp_path


class TestMediaSizes:
    def test_sums_by_top_level_folder(self, media):
        assert exporter.media_sizes(str(media)) == {
            "movies": 150,
            "shows": 7,
            ".": 3,
        }

    def test_empty_folder_reports_zero(self, media):
        (media / "music").mkdir()
        assert exporter.media_sizes(str(media))["music"] == 0

    def test_symlinks_are_not_followed(self, media, tmp_path_factory):
        outside = tmp_path_factory.mktemp("outside")
        write(outside / "big.mkv", 10_000)
        (media / "movies" / "link.mkv").symlink_to(outside / "big.mkv")
        (media / "linked-dir").symlink_to(outside)
        sizes = exporter.media_sizes(str(media))
        assert sizes["movies"] == 150
        assert "linked-dir" not in sizes

    def test_missing_root_raises(self, tmp_path):
        """An unmounted volume must read as a failed scan, not an empty library."""
        with pytest.raises(OSError):
            exporter.media_sizes(str(tmp_path / "nope"))


class TestRefresh:
    def test_failed_scan_keeps_last_sizes(self, media):
        state = exporter.ExpiringCounts()
        exporter.refresh_media(str(media), state)
        exporter.refresh_media(str(media / "nope"), state)
        assert state.snapshot()["movies"] == 150

    def test_loop_scans_immediately(self, media):
        state = exporter.ExpiringCounts()
        stop = threading.Event()
        thread = threading.Thread(
            target=exporter.media_loop, args=(str(media), state, stop, 3600)
        )
        thread.start()
        try:
            for _ in range(100):
                if state.snapshot():
                    break
                stop.wait(0.05)
            assert state.snapshot()["shows"] == 7
        finally:
            stop.set()
            thread.join(timeout=5)
        assert not thread.is_alive()


class TestRender:
    def test_one_series_per_folder(self):
        text = exporter.render(
            [], up=True, last_success=1.0, media={"movies": 150, "shows": 7}
        )
        assert 'jellyfin_media_size_bytes{folder="movies"} 150' in text
        assert 'jellyfin_media_size_bytes{folder="shows"} 7' in text
        assert text.count("# TYPE jellyfin_media_size_bytes gauge") == 1

    def test_omitted_when_unknown(self):
        text = exporter.render([], up=True, last_success=1.0, media=None)
        assert "jellyfin_media_size_bytes" not in text


class TestMain:
    def test_serves_media_without_jellyfin_credentials(self, media, monkeypatch):
        import urllib.request

        for name in ("JELLYFIN_URL", "JELLYFIN_API_KEY"):
            monkeypatch.delenv(name, raising=False)
        monkeypatch.setenv("LISTEN_HOST", "127.0.0.1")
        monkeypatch.setenv("LISTEN_PORT", "0")
        monkeypatch.setenv("MEDIA_PATH", str(media))
        captured = {}

        def fake_serve(server):
            threading.Thread(target=server.serve_forever, daemon=True).start()
            url = f"http://127.0.0.1:{server.server_port}/metrics"
            for _ in range(100):
                with urllib.request.urlopen(url) as resp:
                    captured["metrics"] = resp.read().decode()
                if "jellyfin_media_size_bytes" in captured["metrics"]:
                    break
                threading.Event().wait(0.05)
            server.shutdown()

        assert exporter.main(serve=fake_serve) == 0
        assert 'jellyfin_media_size_bytes{folder="movies"} 150' in captured["metrics"]
        assert "jellyfin_up 0" in captured["metrics"]
