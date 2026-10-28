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

BASE_URL = os.environ.get("ARRS_URL", "http://jellyfin-arrs:{port}")
APPS = {
    "prowlarr": (9696, "v1"),
    "radarr": (7878, "v3"),
    "sonarr": (8989, "v3"),
}
WANTED = {
    "backupFolder": "Backups",
    "backupInterval": int(os.environ.get("BACKUP_INTERVAL_DAYS", "1")),
    "backupRetention": int(os.environ.get("BACKUP_RETENTION_DAYS", "1")),
}


def request(method, url, api_key, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("X-Api-Key", api_key)
    req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as resp:
        payload = resp.read()
    return json.loads(payload) if payload else None


def reconcile(app, port, version, api_key):
    url = f"{BASE_URL.format(port=port)}/api/{version}/config/host"
    current = request("GET", url, api_key)
    changes = {k: v for k, v in WANTED.items() if current.get(k) != v}
    if not changes:
        print(f"{app}: backup settings already up to date")
        return
    # A non-empty password makes the API re-save the auth user; Prowlarr and
    # Radarr return the stored hash here, so never send it back.
    body = {**current, **changes, "password": "", "passwordConfirmation": ""}
    request("PUT", f"{url}/{current['id']}", api_key, body)
    print(f"{app}: updated {changes}")


def main():
    failed = False
    for app, (port, version) in APPS.items():
        api_key = os.environ[f"{app.upper()}_API_KEY"]
        try:
            reconcile(app, port, version, api_key)
        except Exception as e:  # keep going so one app can't block the others
            print(f"{app}: failed: {e}", file=sys.stderr)
            failed = True
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
