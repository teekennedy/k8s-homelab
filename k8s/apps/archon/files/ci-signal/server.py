"""Turns a Woodpecker pipeline notification into an Archon durable-wait signal.

Woodpecker has no outgoing webhooks, so the notification is a step in
`.woodpecker/ci.yaml` and this is what receives it. See README.md for why that
is, and the chart README for the diagram.

This runs as a sidecar in the Archon pod because it needs both halves of it:
/.archon is ReadWriteOnce and shared with the workflow's bash nodes, and it
signals Archon over LOOPBACK -- Archon trusts ARCHON_WEB_AUTH_HEADER as-is, so
anything that can reach its port can claim to be any user. Agents reach :9100
here, never Archon's :3000.

Correlation is by commit SHA. Before each durable wait, arm-ci.sh writes
<state>/waits/<sha>.json naming the Archon run; a notification for that SHA is
matched against it, written to <state>/verdicts/<sha>.json for the workflow's
bash nodes to read, and then signalled through to Archon.

The wait may not be armed yet when the notification lands -- a fast pipeline can
beat the workflow to it -- so delivery retries on a background thread until the
run reports an open `ci.complete` wait, or the deadline passes.
"""

import hashlib
import hmac
import json
import logging
import os
import re
import sys
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Optional

LOG = logging.getLogger("ci-signal")

# A Woodpecker pipeline state we are willing to treat as a verdict. `success` is
# the only green one; the rest are all "the branch is not ready".
TERMINAL_STATES = {"success", "failure", "error", "killed", "declined"}

# Correlation keys are git object names and nothing else, which keeps them
# usable as filenames without any further escaping.
SHA_RE = re.compile(r"^[0-9a-f]{40}$")

MAX_BODY_BYTES = 256 * 1024


@dataclass(frozen=True)
class Config:
    archon_url: str
    archon_user: str
    state_dir: Path
    secret: str
    event: str
    listen_port: int
    signal_deadline_seconds: int
    signal_interval_seconds: int
    arm_grace_seconds: int
    prune_after_seconds: int


CONFIG: Optional[Config] = None


def load_config() -> Config:
    secret = os.environ.get("CI_SIGNAL_SECRET", "")
    if not secret:
        raise SystemExit(
            "CI_SIGNAL_SECRET is required: an unauthenticated signal endpoint is not an option"
        )
    return Config(
        archon_url=os.environ.get("ARCHON_URL", "http://127.0.0.1:3000").rstrip("/"),
        # Archon attributes a request to whoever the trusted header names. This
        # is only ever sent over loopback.
        archon_user=os.environ.get("ARCHON_SIGNAL_USER", "archon"),
        state_dir=Path(os.environ.get("CI_SIGNAL_STATE_DIR", "/.archon/ci-events")),
        secret=secret,
        event=os.environ.get("CI_SIGNAL_EVENT", "ci.complete"),
        listen_port=int(os.environ.get("CI_SIGNAL_PORT", "9100")),
        # Must outlast the wait's own deadline_ms, or a notification that
        # arrives early would stop retrying before the wait is even armed.
        signal_deadline_seconds=int(
            os.environ.get("CI_SIGNAL_DEADLINE_SECONDS", "3600")
        ),
        signal_interval_seconds=int(os.environ.get("CI_SIGNAL_INTERVAL_SECONDS", "5")),
        # Every pull_request pipeline on the repo notifies, not just the
        # agent's, so most notifications correlate to no Archon run at all.
        # Those are dropped after this long rather than retried for an hour.
        arm_grace_seconds=int(os.environ.get("CI_SIGNAL_ARM_GRACE_SECONDS", "180")),
        prune_after_seconds=int(
            os.environ.get("CI_SIGNAL_PRUNE_AFTER_SECONDS", str(7 * 24 * 3600))
        ),
    )


def config() -> Config:
    if CONFIG is None:
        raise RuntimeError("config not loaded")
    return CONFIG


# --- signature ---------------------------------------------------------------


def signature_for(body: bytes, secret: str) -> str:
    """The value `.woodpecker/ci.yaml` sends in X-CI-Signature."""
    return hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


def signature_ok(body: bytes, provided: str, secret: str) -> bool:
    """Constant-time compare, tolerating the `sha256=` prefix some tools add."""
    if not provided:
        return False
    # Lowercased before the prefix is stripped: the digest is hex either way,
    # and tooling is inconsistent about the case of both halves.
    provided = provided.strip().lower()
    if provided.startswith("sha256="):
        provided = provided[len("sha256=") :]
    return hmac.compare_digest(signature_for(body, secret), provided)


# --- payload -----------------------------------------------------------------


@dataclass(frozen=True)
class Notification:
    commit: str
    status: str
    branch: str
    pipeline_number: str
    pipeline_url: str
    repo_id: str


def parse_notification(body: bytes) -> Notification:
    """Validate a Woodpecker notification, raising ValueError on anything off.

    Every field is attacker-influenced as far as this process is concerned --
    the signature proves the sender holds the shared secret, not that the
    contents are sane -- so `commit` is checked against SHA_RE before it is ever
    used as a path component.
    """
    try:
        data = json.loads(body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise ValueError(f"body is not JSON: {exc}") from exc
    if not isinstance(data, dict):
        raise ValueError("body is not a JSON object")

    commit = str(data.get("commit", "")).strip().lower()
    if not SHA_RE.match(commit):
        raise ValueError("commit must be a 40-character hex sha")

    status = str(data.get("status", "")).strip().lower()
    if status not in TERMINAL_STATES:
        raise ValueError(f"status must be one of {sorted(TERMINAL_STATES)}")

    return Notification(
        commit=commit,
        status=status,
        branch=str(data.get("branch", "")),
        pipeline_number=str(data.get("pipeline_number", "")),
        pipeline_url=str(data.get("pipeline_url", "")),
        repo_id=str(data.get("repo_id", "")),
    )


# --- state on the shared volume ----------------------------------------------


def waits_dir() -> Path:
    return config().state_dir / "waits"


def verdicts_dir() -> Path:
    return config().state_dir / "verdicts"


def read_wait(commit: str) -> Optional[dict]:
    """The arming record arm-ci.sh wrote for this commit, if it is there yet."""
    path = waits_dir() / f"{commit}.json"
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return None
    except (OSError, json.JSONDecodeError) as exc:
        LOG.warning("wait record for %s is unreadable: %s", commit, exc)
        return None


def write_verdict(commit: str, verdict: dict) -> None:
    """Publish the verdict for the workflow's bash nodes.

    Written to a temporary file and renamed so a node can never read a half
    written object: ci-green.sh parses this to decide whether the loop is done.
    """
    directory = verdicts_dir()
    directory.mkdir(parents=True, exist_ok=True)
    target = directory / f"{commit}.json"
    staging = directory / f".{commit}.json.tmp"
    staging.write_text(json.dumps(verdict, indent=2, sort_keys=True) + "\n")
    staging.replace(target)


def prune_state(now: Optional[float] = None) -> int:
    """Drop records older than the retention window, returning the count.

    Both directories are on the Archon data PVC, which is also where run
    artifacts live; one small file per pipeline would otherwise accumulate for
    the life of the volume.
    """
    now = now if now is not None else time.time()
    cutoff = now - config().prune_after_seconds
    removed = 0
    for directory in (waits_dir(), verdicts_dir()):
        if not directory.is_dir():
            continue
        for path in directory.iterdir():
            try:
                if path.is_file() and path.stat().st_mtime < cutoff:
                    path.unlink()
                    removed += 1
            except OSError as exc:
                LOG.warning("could not prune %s: %s", path, exc)
    return removed


# --- archon ------------------------------------------------------------------


def archon_request(
    path: str, payload: Optional[dict] = None, timeout: int = 15
) -> dict:
    """One call to Archon over loopback."""
    url = f"{config().archon_url}{path}"
    data = json.dumps(payload).encode() if payload is not None else None
    request = urllib.request.Request(  # noqa: S310 - loopback URL from config, not from a request
        url,
        data=data,
        method="POST" if data is not None else "GET",
        headers={
            "Accept": "application/json",
            # Trusted-header auth, safe only because this is loopback.
            "X-Archon-User": config().archon_user,
            **({"Content-Type": "application/json"} if data is not None else {}),
        },
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:  # noqa: S310
        body = response.read(MAX_BODY_BYTES)
    return json.loads(body) if body else {}


def open_wait(run_id: str) -> Optional[dict]:
    """The run's currently-open event wait, or None if it is not waiting on ours.

    Archon rejects a signal whose resumeAt does not match the wait exactly --
    that is what stops a retry from satisfying a later loop iteration -- so the
    resumeAt is read fresh here rather than remembered from anywhere.
    """
    try:
        run = archon_request(f"/api/workflows/runs/{run_id}").get("run", {})
    except (urllib.error.URLError, OSError, json.JSONDecodeError) as exc:
        LOG.warning("could not read archon run %s: %s", run_id, exc)
        return None
    wait = (run.get("metadata") or {}).get("wait") or {}
    if wait.get("kind") != "event" or wait.get("event") != config().event:
        return None
    if not wait.get("resumeAt"):
        return None
    return wait


def signal_run(run_id: str, resume_at: str, payload: dict) -> bool:
    try:
        archon_request(
            f"/api/workflows/runs/{run_id}/signal",
            {"event": config().event, "resumeAt": resume_at, "payload": payload},
        )
    except urllib.error.HTTPError as exc:
        # 400 is "the run is not waiting on this event" -- normal while the
        # workflow is still between nodes, so it is worth another pass.
        LOG.warning("signal for run %s rejected: HTTP %s", run_id, exc.code)
        return False
    except (urllib.error.URLError, OSError) as exc:
        LOG.warning("signal for run %s failed: %s", run_id, exc)
        return False
    LOG.info("signalled %s on run %s", config().event, run_id)
    return True


def deliver(notification: Notification, verdict: dict) -> None:
    """Retry until the wait exists and accepts the signal, or time runs out.

    The verdict file is already on disk by this point, so a delivery that never
    succeeds degrades to the wait expiring on its own deadline with the evidence
    still there -- not to a lost run.
    """
    cfg = config()
    started = time.time()
    deadline = started + cfg.signal_deadline_seconds
    grace = started + cfg.arm_grace_seconds
    while time.time() < deadline:
        record = read_wait(notification.commit)
        if record is None:
            # arm-ci.sh writes the record just before the wait node runs, so a
            # short window of absence is the race this loop exists for. Beyond
            # it, this is simply a commit no Archon run cares about.
            if time.time() > grace:
                LOG.info(
                    "no archon run is waiting on %s; nothing to signal",
                    notification.commit,
                )
                return
        else:
            run_id = str(record.get("run_id", ""))
            wait = open_wait(run_id) if run_id else None
            if wait and signal_run(run_id, wait["resumeAt"], verdict):
                return
        time.sleep(cfg.signal_interval_seconds)
    LOG.error(
        "gave up signalling for commit %s after %ss; the wait will expire on its own deadline",
        notification.commit,
        cfg.signal_deadline_seconds,
    )


def handle_notification(notification: Notification) -> dict:
    """Record the verdict and hand delivery to a background thread."""
    verdict = {
        "commit": notification.commit,
        "status": notification.status,
        "branch": notification.branch,
        "pipeline_number": notification.pipeline_number,
        "pipeline_url": notification.pipeline_url,
        "repo_id": notification.repo_id,
        "received_at": time.time(),
    }
    write_verdict(notification.commit, verdict)
    LOG.info(
        "verdict for %s: %s (pipeline %s)",
        notification.commit,
        notification.status,
        notification.pipeline_number or "?",
    )
    threading.Thread(target=deliver, args=(notification, verdict), daemon=True).start()
    return verdict


# --- http --------------------------------------------------------------------


class Handler(BaseHTTPRequestHandler):
    server_version = "ci-signal"

    def log_message(
        self, format: str, *args
    ) -> None:  # noqa: A002 - BaseHTTPRequestHandler's signature
        LOG.debug("%s - %s", self.address_string(), format % args)

    def _respond(self, status: int, body: dict) -> None:
        payload = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler's naming
        if self.path == "/healthz":
            self._respond(200, {"ok": True})
            return
        self._respond(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler's naming
        if self.path != "/ci":
            self._respond(404, {"error": "not found"})
            return

        length = int(self.headers.get("Content-Length") or 0)
        if length > MAX_BODY_BYTES:
            self._respond(413, {"error": "body too large"})
            return
        body = self.rfile.read(length)

        if not signature_ok(
            body, self.headers.get("X-CI-Signature", ""), config().secret
        ):
            # Deliberately says nothing about why: the caller either holds the
            # shared secret or has no business here.
            LOG.warning("rejected a notification with a bad signature")
            self._respond(401, {"error": "bad signature"})
            return

        try:
            notification = parse_notification(body)
        except ValueError as exc:
            self._respond(400, {"error": str(exc)})
            return

        handle_notification(notification)
        prune_state()
        # 202, not 200: the signal is delivered on a background thread, and the
        # pipeline step has no reason to block on Archon's scheduler.
        self._respond(202, {"accepted": True, "commit": notification.commit})


def main() -> None:
    global CONFIG  # noqa: PLW0603 - module-level config, set once at startup
    logging.basicConfig(
        level=os.environ.get("LOG_LEVEL", "INFO").upper(),
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
        stream=sys.stdout,
    )
    CONFIG = load_config()
    config().state_dir.mkdir(parents=True, exist_ok=True)
    waits_dir().mkdir(parents=True, exist_ok=True)
    verdicts_dir().mkdir(parents=True, exist_ok=True)

    server = ThreadingHTTPServer(
        ("0.0.0.0", config().listen_port), Handler
    )  # noqa: S104 - fenced by NetworkPolicy
    LOG.info("listening on :%s, state in %s", config().listen_port, config().state_dir)
    server.serve_forever()


if __name__ == "__main__":
    main()
