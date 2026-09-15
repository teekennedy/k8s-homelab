"""Unit tests for the CI-signal relay.

The relay sits between two things that cannot be rehearsed together -- a
Woodpecker pipeline and Archon's durable-wait scheduler -- so the contract at
each edge is pinned here instead: what it accepts, what it refuses, where it
writes, and what it sends on.
"""

import json
import os
import threading
import time
import urllib.error
import urllib.request
from dataclasses import replace
from http.server import ThreadingHTTPServer

import pytest

import server


@pytest.fixture(autouse=True)
def config(tmp_path, monkeypatch):
    """A relay wired to a scratch state directory and a known secret."""
    cfg = server.Config(
        archon_url="http://127.0.0.1:3000",
        archon_user="archon",
        state_dir=tmp_path / "ci-events",
        secret="shared-secret",
        event="ci.complete",
        listen_port=9100,
        signal_deadline_seconds=2,
        signal_interval_seconds=0,
        arm_grace_seconds=1,
        prune_after_seconds=3600,
    )
    monkeypatch.setattr(server, "CONFIG", cfg)
    server.waits_dir().mkdir(parents=True)
    server.verdicts_dir().mkdir(parents=True)
    return cfg


SHA = "a" * 40


def body_for(**overrides) -> bytes:
    payload = {
        "commit": SHA,
        "status": "failure",
        "branch": "agent/abc123",
        "pipeline_number": "42",
        "pipeline_url": "https://ci.msng.to/repos/1/pipeline/42",
        "repo_id": "1",
    }
    payload.update(overrides)
    return json.dumps(payload).encode()


# --- signature ---------------------------------------------------------------


def test_signature_round_trip():
    body = body_for()
    assert server.signature_ok(
        body, server.signature_for(body, "shared-secret"), "shared-secret"
    )


def test_signature_rejects_a_tampered_body():
    """The whole point: the status is not trustworthy without the HMAC."""
    signature = server.signature_for(body_for(status="failure"), "shared-secret")
    assert not server.signature_ok(
        body_for(status="success"), signature, "shared-secret"
    )


def test_signature_rejects_a_wrong_secret():
    body = body_for()
    assert not server.signature_ok(
        body, server.signature_for(body, "other"), "shared-secret"
    )


def test_signature_rejects_an_empty_header():
    assert not server.signature_ok(body_for(), "", "shared-secret")


def test_signature_tolerates_the_sha256_prefix():
    """Plenty of tooling prefixes the digest; rejecting it would be a trap."""
    body = body_for()
    digest = server.signature_for(body, "shared-secret")
    assert server.signature_ok(body, f"sha256={digest}", "shared-secret")
    assert server.signature_ok(body, f"SHA256={digest.upper()}", "shared-secret")


# --- payload validation ------------------------------------------------------


def test_parse_accepts_a_well_formed_notification():
    got = server.parse_notification(body_for())
    assert got.commit == SHA
    assert got.status == "failure"
    assert got.pipeline_number == "42"


@pytest.mark.parametrize(
    "overrides,expected",
    [
        ({"commit": "not-a-sha"}, "40-character hex sha"),
        ({"commit": ""}, "40-character hex sha"),
        # Path traversal through the correlation key, which becomes a filename.
        ({"commit": "../../etc/passwd"}, "40-character hex sha"),
        ({"status": "running"}, "status must be one of"),
        ({"status": ""}, "status must be one of"),
    ],
)
def test_parse_rejects(overrides, expected):
    with pytest.raises(ValueError, match=expected):
        server.parse_notification(body_for(**overrides))


def test_parse_rejects_non_json():
    with pytest.raises(ValueError, match="not JSON"):
        server.parse_notification(b"<html>nope</html>")


def test_parse_rejects_a_json_array():
    with pytest.raises(ValueError, match="not a JSON object"):
        server.parse_notification(b"[]")


def test_parse_normalises_case():
    got = server.parse_notification(body_for(commit=SHA.upper(), status="SUCCESS"))
    assert got.commit == SHA
    assert got.status == "success"


# --- verdict file ------------------------------------------------------------


def test_write_verdict_is_readable_json():
    server.write_verdict(SHA, {"status": "success", "commit": SHA})
    written = json.loads((server.verdicts_dir() / f"{SHA}.json").read_text())
    assert written["status"] == "success"


def test_write_verdict_leaves_no_partial_file():
    """ci-green.sh parses this while the relay may be rewriting it."""
    server.write_verdict(SHA, {"status": "failure"})
    server.write_verdict(SHA, {"status": "success"})
    names = sorted(p.name for p in server.verdicts_dir().iterdir())
    assert names == [f"{SHA}.json"]


def test_read_wait_returns_none_when_not_armed_yet():
    """A pipeline can finish before the workflow reaches its wait node."""
    assert server.read_wait(SHA) is None


def test_read_wait_survives_a_corrupt_record():
    (server.waits_dir() / f"{SHA}.json").write_text("{ truncated")
    assert server.read_wait(SHA) is None


# --- delivery ----------------------------------------------------------------


def arm(commit: str, run_id: str = "run-1") -> None:
    (server.waits_dir() / f"{commit}.json").write_text(
        json.dumps({"run_id": run_id, "event": "ci.complete", "commit": commit})
    )


def test_deliver_signals_the_armed_run(monkeypatch):
    arm(SHA)
    monkeypatch.setattr(
        server, "open_wait", lambda run_id: {"resumeAt": "2026-09-15T00:00:00Z"}
    )
    sent = {}
    monkeypatch.setattr(
        server,
        "signal_run",
        lambda run_id, resume_at, payload: sent.update(
            run=run_id, resume=resume_at, payload=payload
        )
        or True,
    )

    server.deliver(server.parse_notification(body_for()), {"status": "failure"})

    assert sent["run"] == "run-1"
    # Passed back unchanged: Archon compares it exactly, which is what stops a
    # retry from satisfying a later loop iteration.
    assert sent["resume"] == "2026-09-15T00:00:00Z"
    assert sent["payload"] == {"status": "failure"}


def test_deliver_waits_for_a_wait_armed_after_the_notification(monkeypatch):
    """The race this exists for: a fast pipeline beating the wait node."""
    attempts = {"n": 0}

    def open_wait(run_id):
        attempts["n"] += 1
        return {"resumeAt": "2026-09-15T00:00:00Z"} if attempts["n"] > 1 else None

    monkeypatch.setattr(server, "open_wait", open_wait)
    signalled = []
    monkeypatch.setattr(server, "signal_run", lambda *a: signalled.append(a) or True)

    arm(SHA)
    server.deliver(server.parse_notification(body_for()), {"status": "success"})

    assert len(signalled) == 1
    assert attempts["n"] == 2


def test_deliver_drops_a_commit_no_run_is_waiting_for(monkeypatch):
    """Every pull_request pipeline notifies, not just the agent's branches."""
    monkeypatch.setattr(
        server,
        "open_wait",
        lambda run_id: pytest.fail("looked up a run with no arming record"),
    )
    monkeypatch.setattr(
        server, "signal_run", lambda *a: pytest.fail("signalled an uncorrelated commit")
    )

    started = time.monotonic()
    server.deliver(server.parse_notification(body_for()), {"status": "success"})
    # Dropped at the arm grace window (1s here), not held to the full deadline.
    assert time.monotonic() - started < 5


def test_deliver_gives_up_at_the_deadline(monkeypatch):
    """A run that never arms must not pin a thread forever."""
    monkeypatch.setattr(server, "open_wait", lambda run_id: None)
    monkeypatch.setattr(
        server, "signal_run", lambda *a: pytest.fail("signalled without an open wait")
    )
    arm(SHA)

    started = time.monotonic()
    server.deliver(server.parse_notification(body_for()), {"status": "failure"})
    # signal_interval_seconds is 0 in tests; the guard is the deadline itself.
    assert time.monotonic() - started < 10


def test_handle_notification_writes_the_verdict_before_delivering(monkeypatch):
    """The verdict on disk is the durable half; the signal is best effort."""
    seen_on_disk = []
    delivered = threading.Event()

    def fake_deliver(notification, verdict):
        seen_on_disk.append(
            (server.verdicts_dir() / f"{notification.commit}.json").exists()
        )
        delivered.set()

    monkeypatch.setattr(server, "deliver", fake_deliver)

    verdict = server.handle_notification(server.parse_notification(body_for()))

    # deliver() runs on a background thread; wait for it rather than racing it.
    assert delivered.wait(timeout=5)
    written = json.loads((server.verdicts_dir() / f"{SHA}.json").read_text())
    assert written["status"] == "failure"
    assert written["pipeline_number"] == "42"
    assert verdict["commit"] == SHA
    assert seen_on_disk == [True]


# --- open_wait ---------------------------------------------------------------


def test_open_wait_ignores_a_run_waiting_on_something_else(monkeypatch):
    monkeypatch.setattr(
        server,
        "archon_request",
        lambda path, payload=None, timeout=15: {
            "run": {
                "metadata": {
                    "wait": {
                        "kind": "event",
                        "event": "approval.granted",
                        "resumeAt": "x",
                    }
                }
            }
        },
    )
    assert server.open_wait("run-1") is None


def test_open_wait_ignores_a_timer_wait(monkeypatch):
    monkeypatch.setattr(
        server,
        "archon_request",
        lambda path, payload=None, timeout=15: {
            "run": {"metadata": {"wait": {"kind": "timer", "resumeAt": "x"}}}
        },
    )
    assert server.open_wait("run-1") is None


def test_open_wait_ignores_a_run_that_is_not_waiting(monkeypatch):
    monkeypatch.setattr(
        server,
        "archon_request",
        lambda path, payload=None, timeout=15: {"run": {"metadata": {}}},
    )
    assert server.open_wait("run-1") is None


def test_open_wait_returns_the_matching_wait(monkeypatch):
    wait = {"kind": "event", "event": "ci.complete", "resumeAt": "2026-09-15T00:00:00Z"}
    monkeypatch.setattr(
        server,
        "archon_request",
        lambda path, payload=None, timeout=15: {"run": {"metadata": {"wait": wait}}},
    )
    assert server.open_wait("run-1") == wait


# --- pruning -----------------------------------------------------------------


def test_prune_drops_only_old_records(config):
    fresh = server.verdicts_dir() / f"{SHA}.json"
    fresh.write_text("{}")
    stale = server.waits_dir() / f"{'b' * 40}.json"
    stale.write_text("{}")

    old = time.time() - config.prune_after_seconds - 60
    os.utime(stale, (old, old))

    assert server.prune_state() == 1
    assert fresh.exists()
    assert not stale.exists()


def test_prune_tolerates_missing_directories(config, monkeypatch):
    monkeypatch.setattr(
        server, "CONFIG", replace(config, state_dir=config.state_dir / "nope")
    )
    assert server.prune_state() == 0


# --- config ------------------------------------------------------------------


def test_load_config_refuses_to_start_without_a_secret(monkeypatch):
    """An unauthenticated signal endpoint would let anyone resume any run."""
    monkeypatch.delenv("CI_SIGNAL_SECRET", raising=False)
    with pytest.raises(SystemExit, match="CI_SIGNAL_SECRET"):
        server.load_config()


def test_load_config_reads_the_environment(monkeypatch):
    monkeypatch.setenv("CI_SIGNAL_SECRET", "s3cret")
    monkeypatch.setenv("ARCHON_URL", "http://127.0.0.1:3000/")
    monkeypatch.setenv("CI_SIGNAL_PORT", "9999")

    cfg = server.load_config()

    # Trailing slash stripped, or every path would be built with a double one.
    assert cfg.archon_url == "http://127.0.0.1:3000"
    assert cfg.listen_port == 9999
    assert cfg.secret == "s3cret"


# --- http --------------------------------------------------------------------


@pytest.fixture
def relay(monkeypatch):
    """The real Handler on a real socket: the edge Woodpecker actually posts to."""
    # Only the request path is under test; delivery has its own tests, and
    # letting it run would dial a loopback Archon that is not there.
    monkeypatch.setattr(server, "deliver", lambda notification, verdict: None)
    httpd = ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{httpd.server_address[1]}"
    finally:
        httpd.shutdown()
        httpd.server_close()
        thread.join(timeout=5)


def post(base: str, body: bytes, signature: str) -> tuple[int, dict]:
    request = urllib.request.Request(
        f"{base}/ci",
        data=body,
        method="POST",
        headers={"Content-Type": "application/json", "X-CI-Signature": signature},
    )
    try:
        with urllib.request.urlopen(request, timeout=5) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as exc:
        return exc.code, json.loads(exc.read())


def test_post_accepts_a_signed_notification(relay):
    body = body_for()
    status, payload = post(relay, body, server.signature_for(body, "shared-secret"))

    # 202, not 200: the signal is delivered on a background thread.
    assert status == 202
    assert payload == {"accepted": True, "commit": SHA}
    assert (server.verdicts_dir() / f"{SHA}.json").exists()


def test_post_rejects_a_bad_signature(relay):
    """The only thing standing between this port and any pod in the namespace."""
    status, payload = post(relay, body_for(), "deadbeef")

    assert status == 401
    # Says nothing about why: the caller either holds the secret or does not.
    assert payload == {"error": "bad signature"}
    assert not list(server.verdicts_dir().iterdir())


def test_post_rejects_a_signed_but_malformed_body(relay):
    body = b'{"commit": "nope", "status": "success"}'
    status, payload = post(relay, body, server.signature_for(body, "shared-secret"))

    assert status == 400
    assert "40-character hex sha" in payload["error"]


def test_healthz_is_the_probe(relay):
    with urllib.request.urlopen(f"{relay}/healthz", timeout=5) as response:
        assert response.status == 200
        assert json.loads(response.read()) == {"ok": True}


def test_unknown_paths_are_404(relay):
    for path in ("/", "/ci/../etc"):
        try:
            urllib.request.urlopen(f"{relay}{path}", timeout=5)
        except urllib.error.HTTPError as exc:
            assert exc.code == 404
        else:
            pytest.fail(f"{path} should not be served")
