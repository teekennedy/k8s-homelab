"""Set the arr apps' backup schedule, which they keep in their database.

The backup folder is left at its default (/config/Backups, the NFS mount).
Interval and retention are both one day: each scheduled backup first deletes
backups older than the retention, so only the latest one is kept on the NAS.
Older copies live in restic's snapshots of /storage/nas/backups.
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


def wanted_settings(env) -> dict:
    return {
        "backupFolder": "Backups",
        "backupInterval": int(env.get("BACKUP_INTERVAL_DAYS", "1")),
        "backupRetention": int(env.get("BACKUP_RETENTION_DAYS", "1")),
    }


def request(method: str, url: str, api_key: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("X-Api-Key", api_key)
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as resp:
        payload = resp.read()
    return json.loads(payload) if payload else None


def reconcile(app: str, url: str, api_key: str, wanted: dict) -> dict:
    """Bring the app's host config at url in line with wanted.

    Returns the settings that were changed.
    """
    current = request("GET", url, api_key)
    changes = {k: v for k, v in wanted.items() if current.get(k) != v}
    if not changes:
        print(f"{app}: backup settings already up to date")
        return changes
    # A non-empty password makes the API re-save the auth user; Prowlarr and
    # Radarr return the stored hash here, so never send it back.
    body = {**current, **changes, "password": "", "passwordConfirmation": ""}
    request("PUT", f"{url}/{current['id']}", api_key, body)
    print(f"{app}: updated {changes}")
    return changes


def main(env=os.environ) -> int:
    base_url = env.get("ARRS_URL", DEFAULT_BASE_URL)
    wanted = wanted_settings(env)
    failed = False
    for app, (port, version) in APPS.items():
        url = f"{base_url.format(port=port)}/api/{version}/config/host"
        try:
            reconcile(app, url, env[f"{app.upper()}_API_KEY"], wanted)
        except Exception as e:  # keep going so one app can't block the others
            print(f"{app}: failed: {e!r}", file=sys.stderr)
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
