"""Create a Jellyfin server backup and delete the older ones.

Jellyfin writes the archive to its own backup folder, which is the same NFS
directory this job mounts at BACKUP_DIR. Jellyfin's archive leaves out
plugins, so this job also archives them from the data volume (DATA_DIR) into
an extras tar with the same timestamp. Only the newest of each is kept; restic
snapshots the NAS backup folder for history.
"""

import contextlib
import json
import os
import sqlite3
import sys
import tarfile
import tempfile
import urllib.request
from pathlib import Path

# The database, config, users, libraries, collections and playlists are always
# included. Metadata and trickplay can be refetched or regenerated.
OPTIONS = {"Database": True, "Subtitles": True, "Metadata": False, "Trickplay": False}
ARCHIVE_GLOB = "jellyfin-backup-*.zip"
# Databases in DATA_DIR/data left out of the extras tar. Every other one there
# belongs to a plugin and is copied.
SKIPPED_DATABASES = {
    # Already in Jellyfin's own archive.
    "jellyfin.db",
    # Infuse's sync cache, which the plugin rebuilds. It's in WAL mode, so
    # SQLite can't open it from a read-only mount while it's closed.
    "infuse_sync.db",
}


def create_backup(jellyfin_url: str, api_key: str) -> dict:
    """Ask Jellyfin for a backup and return its manifest."""
    req = urllib.request.Request(
        f"{jellyfin_url}/Backup/Create",
        data=json.dumps(OPTIONS).encode(),
        method="POST",
    )
    req.add_header("Authorization", f'MediaBrowser Token="{api_key}"')
    req.add_header("Content-Type", "application/json")
    # The request returns once the archive is written.
    with urllib.request.urlopen(req, timeout=3600) as resp:
        return json.loads(resp.read())


def snapshot_sqlite(src: Path, dest: Path) -> None:
    """Copy a live SQLite database to dest without writing to src."""
    with (
        contextlib.closing(sqlite3.connect(f"{src.as_uri()}?mode=ro", uri=True)) as s,
        contextlib.closing(sqlite3.connect(dest)) as d,
    ):
        s.backup(d)


def write_extras(data_dir: Path, dest: Path) -> None:
    """Archive plugins and plugin databases from data_dir to dest.

    The tar is written beside dest and renamed into place, so dest is only
    ever a complete archive.
    """
    tmp = dest.with_name(f".{dest.name}.tmp")
    try:
        with (
            tempfile.TemporaryDirectory() as scratch,
            tarfile.open(tmp, "w") as tar,
        ):
            # Installed plugins and their settings, including credentials.
            tar.add(data_dir / "plugins", arcname="plugins")
            # Copied through SQLite so that a write mid-copy can't tear them.
            for db in sorted((data_dir / "data").glob("*.db")):
                if db.name not in SKIPPED_DATABASES:
                    copy = Path(scratch) / db.name
                    snapshot_sqlite(db, copy)
                    tar.add(copy, arcname=f"data/{db.name}")
        tmp.chmod(0o600)
        tmp.rename(dest)
    except BaseException:
        tmp.unlink(missing_ok=True)
        raise


def prune(backup_dir: Path, pattern: str, keep: str) -> list[str]:
    """Delete every file in backup_dir matching pattern except keep."""
    deleted = []
    for old in sorted(backup_dir.glob(pattern)):
        if old.name != keep:
            old.unlink()
            deleted.append(old.name)
    return deleted


def keep_only(backup_dir: Path, pattern: str, name: str) -> None:
    """Report name as created and delete the older files matching pattern."""
    print(f"created {name}")
    for old in prune(backup_dir, pattern, name):
        print(f"deleted {old}")


def main(env=os.environ) -> None:
    backup_dir = Path(env["BACKUP_DIR"])
    manifest = create_backup(env["JELLYFIN_URL"], env["JELLYFIN_API_KEY"])
    # Path is where Jellyfin wrote it, inside its own container.
    latest = Path(manifest["Path"])
    if not (backup_dir / latest.name).is_file():
        sys.exit(f"{latest.name} not found in {backup_dir}; not deleting anything")
    keep_only(backup_dir, ARCHIVE_GLOB, latest.name)

    stamp = latest.stem.removeprefix("jellyfin-backup-")
    extras = f"jellyfin-extras-{stamp}.tar"
    write_extras(Path(env["DATA_DIR"]), backup_dir / extras)
    keep_only(backup_dir, "jellyfin-extras-*.tar", extras)


if __name__ == "__main__":
    main()
