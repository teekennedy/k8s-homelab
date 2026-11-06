import urllib.error

import pytest

import backup
from conftest import API_KEY


def test_create_backup_sends_options_and_token(jellyfin):
    manifest = backup.create_backup(jellyfin.url, API_KEY)

    assert manifest["Path"].endswith(jellyfin.archive)
    (req,) = jellyfin.requests
    assert req["path"] == "/Backup/Create"
    assert req["headers"]["Authorization"] == f'MediaBrowser Token="{API_KEY}"'
    assert req["body"] == backup.OPTIONS


def test_main_keeps_only_the_new_archive(jellyfin, capsys):
    old = jellyfin.backup_dir / "jellyfin-backup-20261105040000.zip"
    old.write_bytes(b"old")

    backup.main(jellyfin.env)

    archives = sorted(p.name for p in jellyfin.backup_dir.iterdir())
    assert archives == [jellyfin.archive]
    out = capsys.readouterr().out
    assert f"created {jellyfin.archive}" in out
    assert f"deleted {old.name}" in out


def test_main_leaves_unrelated_files_alone(jellyfin):
    other = jellyfin.backup_dir / "notes.txt"
    other.write_text("keep me")

    backup.main(jellyfin.env)

    assert other.exists()


def test_main_deletes_nothing_when_archive_is_missing(jellyfin):
    jellyfin.write_archive = False
    old = jellyfin.backup_dir / "jellyfin-backup-20261105040000.zip"
    old.write_bytes(b"old")

    with pytest.raises(SystemExit) as exc:
        backup.main(jellyfin.env)

    assert "not deleting anything" in str(exc.value.code)
    assert old.exists()


def test_main_deletes_nothing_when_jellyfin_fails(jellyfin):
    jellyfin.status = 500
    old = jellyfin.backup_dir / "jellyfin-backup-20261105040000.zip"
    old.write_bytes(b"old")

    with pytest.raises(urllib.error.HTTPError):
        backup.main(jellyfin.env)

    assert old.exists()


def test_prune_returns_deleted_names(tmp_path):
    for name in ("jellyfin-backup-1.zip", "jellyfin-backup-2.zip"):
        (tmp_path / name).write_bytes(b"")

    assert backup.prune(tmp_path, "jellyfin-backup-2.zip") == ["jellyfin-backup-1.zip"]
    assert [p.name for p in tmp_path.iterdir()] == ["jellyfin-backup-2.zip"]
