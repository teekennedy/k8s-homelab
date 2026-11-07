import contextlib
import json
import sqlite3
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

API_KEY = "test-api-key"


class StubJellyfin:
    """Answers POST /Backup/Create by writing an archive into backup_dir."""

    def __init__(self, backup_dir: Path):
        self.backup_dir = backup_dir
        self.requests: list[dict] = []
        self.archive = "jellyfin-backup-20261106040000.zip"
        # Set False to report an archive that never reaches backup_dir.
        self.write_archive = True
        self.status = 200


@pytest.fixture
def data_dir(tmp_path):
    """A Jellyfin data volume holding the files the extras tar picks up."""
    data = tmp_path / "data"
    (data / "plugins/configurations").mkdir(parents=True)
    (data / "plugins/configurations/LDAP-Auth.xml").write_text("<ldap/>")
    (data / "plugins/SSO-Auth").mkdir()
    (data / "plugins/SSO-Auth/sso-secret.key").write_text("secret")
    (data / "data").mkdir()
    # Already in Jellyfin's own archive, so left out of the extras tar.
    (data / "data/jellyfin.db").write_bytes(b"core")
    with contextlib.closing(sqlite3.connect(data / "data/playback_reporting.db")) as db:
        db.execute("CREATE TABLE PlaybackActivity (ItemName TEXT)")
        db.execute("INSERT INTO PlaybackActivity VALUES ('Heat')")
        db.commit()
    return data


@pytest.fixture
def jellyfin(tmp_path, data_dir):
    backup_dir = tmp_path / "backups"
    backup_dir.mkdir()
    stub = StubJellyfin(backup_dir)

    class Handler(BaseHTTPRequestHandler):
        def do_POST(self):
            length = int(self.headers.get("Content-Length", 0))
            stub.requests.append(
                {
                    "path": self.path,
                    "headers": dict(self.headers),
                    "body": json.loads(self.rfile.read(length)),
                }
            )
            if stub.status != 200:
                self.send_error(stub.status)
                return
            if stub.write_archive:
                (stub.backup_dir / stub.archive).write_bytes(b"zip")
            payload = json.dumps(
                {"Path": f"/data/data/backups/{stub.archive}"}
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, *args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    stub.url = f"http://127.0.0.1:{server.server_port}"
    stub.env = {
        "JELLYFIN_URL": stub.url,
        "JELLYFIN_API_KEY": API_KEY,
        "BACKUP_DIR": str(backup_dir),
        "DATA_DIR": str(data_dir),
    }
    yield stub
    server.shutdown()
    server.server_close()
