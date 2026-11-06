"""Create a Jellyfin server backup and delete the older ones.

Jellyfin writes the archive to its own backup folder, which is the same NFS
directory this job mounts at BACKUP_DIR. Only the new archive is kept; restic
snapshots the NAS backup folder for history.
"""

import json
import os
import sys
import urllib.request
from pathlib import Path

# The database, config, users, libraries, collections and playlists are always
# included. Metadata and trickplay can be refetched or regenerated.
OPTIONS = {"Database": True, "Subtitles": True, "Metadata": False, "Trickplay": False}
ARCHIVE_GLOB = "jellyfin-backup-*.zip"


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


def prune(backup_dir: Path, keep: str) -> list[str]:
    """Delete every backup archive in backup_dir except keep."""
    deleted = []
    for old in sorted(backup_dir.glob(ARCHIVE_GLOB)):
        if old.name != keep:
            old.unlink()
            deleted.append(old.name)
    return deleted


def main(env=os.environ) -> None:
    backup_dir = Path(env["BACKUP_DIR"])
    manifest = create_backup(env["JELLYFIN_URL"], env["JELLYFIN_API_KEY"])
    # Path is where Jellyfin wrote it, inside its own container.
    latest = Path(manifest["Path"]).name
    if not (backup_dir / latest).is_file():
        sys.exit(f"{latest} not found in {backup_dir}; not deleting anything")
    print(f"created {latest}")
    for name in prune(backup_dir, latest):
        print(f"deleted {name}")


if __name__ == "__main__":
    main()
