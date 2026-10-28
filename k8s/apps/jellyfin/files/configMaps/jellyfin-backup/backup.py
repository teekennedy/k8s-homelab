"""Create a Jellyfin server backup and delete the older ones.

Jellyfin writes the archive to its own backup folder (/data/data/backups),
which is the same NFS directory this job mounts at BACKUP_DIR. Only the new
archive is kept; restic snapshots the NAS backup folder for history.
"""

import json
import os
import sys
import urllib.request
from pathlib import Path

JELLYFIN_URL = os.environ["JELLYFIN_URL"]
API_KEY = os.environ["JELLYFIN_API_KEY"]
BACKUP_DIR = Path(os.environ["BACKUP_DIR"])
# The database, config, users, libraries, collections and playlists are always
# included. Metadata and trickplay can be refetched or regenerated.
OPTIONS = {"Database": True, "Subtitles": True, "Metadata": False, "Trickplay": False}


def create_backup():
    req = urllib.request.Request(
        f"{JELLYFIN_URL}/Backup/Create",
        data=json.dumps(OPTIONS).encode(),
        method="POST",
    )
    req.add_header("Authorization", f'MediaBrowser Token="{API_KEY}"')
    req.add_header("Content-Type", "application/json")
    # The request returns once the archive is written.
    with urllib.request.urlopen(req, timeout=3600) as resp:
        return json.loads(resp.read())


def main():
    manifest = create_backup()
    latest = Path(manifest["Path"]).name
    if not (BACKUP_DIR / latest).is_file():
        sys.exit(f"{latest} not found in {BACKUP_DIR}; not deleting anything")
    print(f"created {latest}")
    for old in BACKUP_DIR.glob("jellyfin-backup-*.zip"):
        if old.name != latest:
            old.unlink()
            print(f"deleted {old.name}")


if __name__ == "__main__":
    main()
