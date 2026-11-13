import reconcile
from conftest import API_KEY

WANTED = reconcile.wanted_settings({})


def test_wanted_settings_defaults_to_one_day():
    assert WANTED == {
        "backupFolder": "Backups",
        "backupInterval": 1,
        "backupRetention": 1,
    }


def test_wanted_settings_reads_env():
    wanted = reconcile.wanted_settings(
        {"BACKUP_INTERVAL_DAYS": "3", "BACKUP_RETENTION_DAYS": "14"}
    )

    assert wanted["backupInterval"] == 3
    assert wanted["backupRetention"] == 14


def test_reconcile_updates_only_changed_settings(arr):
    changes = reconcile.reconcile("radarr", arr.url, API_KEY, WANTED)

    assert changes == {"backupInterval": 1, "backupRetention": 1}
    get, put = arr.requests
    assert get["method"] == "GET"
    assert put["method"] == "PUT"
    assert put["path"].endswith("/config/host/1")
    assert put["headers"]["X-Api-Key"] == API_KEY
    assert arr.config["backupInterval"] == 1
    assert arr.config["backupRetention"] == 1


def test_reconcile_keeps_other_settings(arr):
    reconcile.reconcile("radarr", arr.url, API_KEY, WANTED)

    body = arr.requests[-1]["body"]
    assert body["bindAddress"] == "*"
    assert body["port"] == 7878
    assert body["username"] == "admin"


def test_reconcile_sends_stored_password_back_unchanged(arr):
    reconcile.reconcile("radarr", arr.url, API_KEY, WANTED)

    body = arr.requests[-1]["body"]
    assert body["password"] == "stored-hash"


def test_reconcile_is_a_noop_when_up_to_date(arr, capsys):
    arr.config.update(WANTED)

    assert reconcile.reconcile("radarr", arr.url, API_KEY, WANTED) == {}

    assert [r["method"] for r in arr.requests] == ["GET"]
    assert "already up to date" in capsys.readouterr().out


def test_wanted_recycle_bin_is_per_app():
    assert reconcile.wanted_recycle_bin("sonarr", {}) == {
        "recycleBin": "/data/media/.recycle/sonarr",
        "recycleBinCleanupDays": 14,
    }
    assert (
        reconcile.wanted_recycle_bin("radarr", {"RECYCLE_BIN_DAYS": "3"})[
            "recycleBinCleanupDays"
        ]
        == 3
    )


def test_reconcile_sets_the_recycle_bin(arr):
    arr.config.update(recycleBin="", recycleBinCleanupDays=7)
    wanted = reconcile.wanted_recycle_bin("radarr", {})

    changes = reconcile.reconcile("radarr", arr.url, API_KEY, wanted, "recycle bin")

    assert changes == wanted
    assert arr.config["recycleBin"] == "/data/media/.recycle/radarr"
    assert arr.config["backupInterval"] == 7


def test_main_builds_each_apps_url(monkeypatch):
    calls = []
    monkeypatch.setattr(
        reconcile,
        "reconcile",
        lambda app, url, key, wanted, what: calls.append((app, url)),
    )
    env = {
        "ARRS_URL": "http://arrs:{port}",
        "PROWLARR_API_KEY": "p",
        "RADARR_API_KEY": "r",
        "SONARR_API_KEY": "s",
    }

    assert reconcile.main(env) == 0
    assert calls == [
        ("prowlarr", "http://arrs:9696/api/v1/config/host"),
        ("radarr", "http://arrs:7878/api/v3/config/host"),
        ("radarr", "http://arrs:7878/api/v3/config/mediamanagement"),
        ("sonarr", "http://arrs:8989/api/v3/config/host"),
        ("sonarr", "http://arrs:8989/api/v3/config/mediamanagement"),
    ]


def test_main_keeps_going_after_a_failure(monkeypatch, capsys):
    calls = []

    def fake(app, url, key, wanted, what):
        calls.append(app)
        if app == "prowlarr":
            raise ConnectionError("refused")

    monkeypatch.setattr(reconcile, "reconcile", fake)
    env = {"PROWLARR_API_KEY": "p", "RADARR_API_KEY": "r", "SONARR_API_KEY": "s"}

    assert reconcile.main(env) == 1
    assert calls == ["prowlarr", "radarr", "radarr", "sonarr", "sonarr"]
    assert "prowlarr: backup settings failed" in capsys.readouterr().err


def test_main_reports_a_missing_api_key_and_continues(monkeypatch, capsys):
    calls = []
    monkeypatch.setattr(
        reconcile, "reconcile", lambda app, url, key, wanted, what: calls.append(app)
    )

    assert reconcile.main({"RADARR_API_KEY": "r", "SONARR_API_KEY": "s"}) == 1
    assert calls == ["radarr", "radarr", "sonarr", "sonarr"]
    assert "PROWLARR_API_KEY" in capsys.readouterr().err
