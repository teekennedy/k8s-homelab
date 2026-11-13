"""Set arr app settings that configarr doesn't manage, which the apps keep in
their database.

Backups: the folder is left at its default (/config/Backups, the NFS mount).
Interval and retention are both one day: each scheduled backup first deletes
backups older than the retention, so only the latest one is kept on the NAS.
Older copies live in restic's snapshots of /storage/nas/backups.

Recycle bins: Radarr and Sonarr move deleted files to
/data/media/.recycle/<app> instead of deleting them, and empty it after
RECYCLE_BIN_DAYS (14). The NAS has no snapshots, so this is the only undo.
"""

import json
import os
import sys
import urllib.request

DEFAULT_BASE_URL = "http://jellyfin-arrs:{port}"
APPS = {
    "prowlarr": (9696, "v1"),
    "radarr": (7878, "v3"),
    "sonarr": (8989, "v3"),
}


# Apps with a recycle bin, under their media management settings.
RECYCLING_APPS = ("radarr", "sonarr")


def wanted_settings(env) -> dict:
    return {
        "backupFolder": "Backups",
        "backupInterval": int(env.get("BACKUP_INTERVAL_DAYS", "1")),
        "backupRetention": int(env.get("BACKUP_RETENTION_DAYS", "1")),
    }


def wanted_recycle_bin(app: str, env) -> dict:
    return {
        "recycleBin": f"/data/media/.recycle/{app}",
        "recycleBinCleanupDays": int(env.get("RECYCLE_BIN_DAYS", "14")),
    }


def request(method: str, url: str, api_key: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("X-Api-Key", api_key)
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as resp:
        payload = resp.read()
    return json.loads(payload) if payload else None


def reconcile(
    app: str, url: str, api_key: str, wanted: dict, what: str = "backup settings"
) -> dict:
    """Bring the app's config resource at url in line with wanted.

    Returns the settings that were changed.
    """
    current = request("GET", url, api_key)
    changes = {k: v for k, v in wanted.items() if current.get(k) != v}
    if not changes:
        print(f"{app}: {what} already up to date")
        return changes
    # The whole resource goes back. For the host config that includes the
    # stored password hash: the API rejects an empty password when
    # authentication is on, and an unchanged hash leaves the user as is.
    body = {**current, **changes}
    request("PUT", f"{url}/{current['id']}", api_key, body)
    print(f"{app}: updated {what} {changes}")
    return changes


def main(env=os.environ) -> int:
    base_url = env.get("ARRS_URL", DEFAULT_BASE_URL)
    wanted = wanted_settings(env)
    failed = False
    for app, (port, version) in APPS.items():
        api = f"{base_url.format(port=port)}/api/{version}/config"
        tasks = [(f"{api}/host", wanted, "backup settings")]
        if app in RECYCLING_APPS:
            tasks.append(
                (f"{api}/mediamanagement", wanted_recycle_bin(app, env), "recycle bin")
            )
        for url, settings, what in tasks:
            try:
                reconcile(app, url, env[f"{app.upper()}_API_KEY"], settings, what)
            except Exception as e:  # keep going so one app can't block the others
                print(f"{app}: {what} failed: {e!r}", file=sys.stderr)
                failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
