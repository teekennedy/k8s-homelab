import contextlib
import sqlite3
import tarfile
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


@pytest.mark.parametrize("pattern", ["jellyfin-backup-*.zip", "jellyfin-extras-*.tar"])
def test_main_keeps_only_the_newest(jellyfin, capsys, pattern):
    old = jellyfin.backup_dir / pattern.replace("*", "20261105040000")
    old.write_bytes(b"old")

    backup.main(jellyfin.env)

    (new,) = jellyfin.backup_dir.glob(pattern)
    assert new.name.removesuffix(new.suffix).endswith("20261106040000")
    out = capsys.readouterr().out
    assert f"created {new.name}" in out
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

    deleted = backup.prune(tmp_path, backup.ARCHIVE_GLOB, "jellyfin-backup-2.zip")

    assert deleted == ["jellyfin-backup-1.zip"]
    assert [p.name for p in tmp_path.iterdir()] == ["jellyfin-backup-2.zip"]


def test_main_writes_extras_with_plugins_and_plugin_databases(jellyfin, tmp_path):
    backup.main(jellyfin.env)

    (extras,) = jellyfin.backup_dir.glob("jellyfin-extras-*.tar")
    assert extras.stat().st_mode & 0o777 == 0o600
    out = tmp_path / "restore"
    with tarfile.open(extras) as tar:
        tar.extractall(out, filter="data")
    assert (out / "plugins/configurations/LDAP-Auth.xml").read_text() == "<ldap/>"
    assert (out / "plugins/SSO-Auth/sso-secret.key").read_text() == "secret"
    with contextlib.closing(sqlite3.connect(out / "data/playback_reporting.db")) as db:
        rows = db.execute("SELECT ItemName FROM PlaybackActivity").fetchall()
    assert rows == [("Heat",)]
    assert not (out / "data/jellyfin.db").exists()


def test_failed_extras_leave_old_extras_and_no_partial_file(jellyfin, data_dir):
    (data_dir / "data/broken.db").write_bytes(b"not a database" * 100)
    old = jellyfin.backup_dir / "jellyfin-extras-20261105040000.tar"
    old.write_bytes(b"old")

    with pytest.raises(sqlite3.DatabaseError):
        backup.main(jellyfin.env)

    leftovers = sorted(p.name for p in jellyfin.backup_dir.iterdir())
    assert leftovers == [jellyfin.archive, old.name]
