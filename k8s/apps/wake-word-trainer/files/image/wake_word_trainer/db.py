"""The collector's SQLite database: one row per clip, one per training run.

Only the collector process opens it (it lives on a ReadWriteOnce Longhorn
volume, never NFS); the trainer goes through the collector's internal API.
"""

import hashlib
import json
import sqlite3
import threading
import time
import uuid
from contextlib import contextmanager
from pathlib import Path

from .settings import POSITIVE_LABELS

SCHEMA = """
CREATE TABLE IF NOT EXISTS clips (
    id TEXT PRIMARY KEY,
    device TEXT NOT NULL,
    received_at REAL NOT NULL,
    label_sent TEXT NOT NULL,
    label TEXT NOT NULL,
    path TEXT NOT NULL,
    duration_s REAL NOT NULL,
    split TEXT NOT NULL,
    -- pending -> processed | error
    status TEXT NOT NULL DEFAULT 'pending',
    transcript TEXT,
    words TEXT,
    -- pending | auto | flagged | confirmed | discarded
    review TEXT NOT NULL DEFAULT 'pending',
    flag_reason TEXT,
    reviewed_by TEXT,
    reviewed_at REAL,
    derived TEXT,
    error TEXT,
    first_run_id TEXT
);
CREATE INDEX IF NOT EXISTS clips_device_label ON clips (device, label, received_at);
CREATE INDEX IF NOT EXISTS clips_status ON clips (status);
CREATE INDEX IF NOT EXISTS clips_review ON clips (review);

CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    started_at REAL NOT NULL,
    finished_at REAL,
    -- running | succeeded | failed
    status TEXT NOT NULL,
    forced INTEGER NOT NULL DEFAULT 0,
    gate TEXT,
    promoted INTEGER,
    published TEXT,
    result TEXT,
    error TEXT
);

CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT
);
"""

SPLITS = ("training", "validation", "testing")


def split_for(clip_id: str) -> str:
    """60/20/20, fixed by the id forever: the test set must stay comparable."""
    bucket = int(hashlib.sha256(clip_id.encode()).hexdigest(), 16) % 10
    if bucket < 6:
        return "training"
    return "validation" if bucket < 8 else "testing"


def _row(r: sqlite3.Row | None) -> dict | None:
    if r is None:
        return None
    d = dict(r)
    for key in ("words", "derived", "gate", "result", "published"):
        if d.get(key):
            d[key] = json.loads(d[key])
    return d


class Store:
    def __init__(self, path: Path):
        path.parent.mkdir(parents=True, exist_ok=True)
        self._conn = sqlite3.connect(
            str(path), check_same_thread=False, isolation_level=None
        )
        self._conn.row_factory = sqlite3.Row
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA busy_timeout=5000")
        self._conn.executescript(SCHEMA)
        self._lock = threading.RLock()

    @contextmanager
    def tx(self):
        with self._lock:
            self._conn.execute("BEGIN IMMEDIATE")
            try:
                yield self._conn
                self._conn.execute("COMMIT")
            except BaseException:
                self._conn.execute("ROLLBACK")
                raise

    def query(self, sql: str, *args) -> list[dict]:
        with self._lock:
            return [_row(r) for r in self._conn.execute(sql, args).fetchall()]

    def one(self, sql: str, *args) -> dict | None:
        rows = self.query(sql, *args)
        return rows[0] if rows else None

    # ---------------------------------------------------------------- clips

    def add_clip(
        self,
        clip_id: str,
        device: str,
        label: str,
        path: str,
        duration_s: float,
        now: float,
    ):
        with self.tx() as c:
            c.execute(
                "INSERT INTO clips (id, device, received_at, label_sent, label, path,"
                " duration_s, split) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    clip_id,
                    device,
                    now,
                    label,
                    label,
                    path,
                    duration_s,
                    split_for(clip_id),
                ),
            )

    def relabel_last(
        self, device: str, label: str, now: float, window_s: float
    ) -> str | None:
        """Move the device's newest recent `detected` clip to `label`; its id, or None."""
        with self.tx() as c:
            r = c.execute(
                "SELECT id FROM clips WHERE device = ? AND label = 'detected'"
                " AND received_at >= ? ORDER BY received_at DESC LIMIT 1",
                (device, now - window_s),
            ).fetchone()
            if r is None:
                return None
            c.execute(
                "UPDATE clips SET label = ?, status = 'pending', review = 'pending',"
                " derived = NULL, flag_reason = NULL WHERE id = ?",
                (label, r["id"]),
            )
            return r["id"]

    def discard_recent_ambient(
        self, device: str, now: float, window_s: float
    ) -> list[str]:
        with self.tx() as c:
            ids = [
                r["id"]
                for r in c.execute(
                    "SELECT id FROM clips WHERE device = ? AND label = 'true_reject'"
                    " AND received_at >= ?",
                    (device, now - window_s),
                )
            ]
            for i in ids:
                c.execute(
                    "UPDATE clips SET label = 'discarded', review = 'discarded',"
                    " flag_reason = 'followed by another upload within 30 s' WHERE id = ?",
                    (i,),
                )
            return ids

    def next_pending(self) -> dict | None:
        return self.one(
            "SELECT * FROM clips WHERE status = 'pending' AND label != 'discarded'"
            " ORDER BY received_at LIMIT 1"
        )

    def save_processing(
        self,
        clip_id: str,
        label: str,
        transcript: str,
        words: list,
        flags: list[str],
        derived,
    ) -> bool:
        """Store the processor's result, unless the label changed meanwhile."""
        with self.tx() as c:
            r = c.execute(
                "SELECT label, reviewed_by FROM clips WHERE id = ?", (clip_id,)
            ).fetchone()
            if r is None or r["label"] != label:
                return False
            if r["reviewed_by"]:
                review = "confirmed"
            else:
                review = "flagged" if flags else "auto"
            c.execute(
                "UPDATE clips SET status = 'processed', transcript = ?, words = ?,"
                " review = ?, flag_reason = ?, derived = ?, error = NULL WHERE id = ?",
                (
                    transcript,
                    json.dumps(words),
                    review,
                    "; ".join(flags) or None,
                    json.dumps(derived),
                    clip_id,
                ),
            )
            return True

    def save_error(self, clip_id: str, error: str):
        with self.tx() as c:
            c.execute(
                "UPDATE clips SET status = 'error', error = ? WHERE id = ?",
                (error[-2000:], clip_id),
            )

    def review(
        self, clip_id: str, action: str, user: str, now: float, label: str | None = None
    ):
        """confirm | relabel | discard | restore, by a person."""
        with self.tx() as c:
            r = c.execute("SELECT label FROM clips WHERE id = ?", (clip_id,)).fetchone()
            if r is None:
                raise KeyError(clip_id)
            if action == "confirm":
                c.execute(
                    "UPDATE clips SET review = 'confirmed', reviewed_by = ?, reviewed_at = ?"
                    " WHERE id = ? AND status = 'processed'",
                    (user, now, clip_id),
                )
            elif action == "relabel":
                c.execute(
                    "UPDATE clips SET label = ?, status = 'pending', review = 'pending',"
                    " derived = NULL, flag_reason = NULL, reviewed_by = ?, reviewed_at = ?"
                    " WHERE id = ?",
                    (label, user, now, clip_id),
                )
            elif action == "discard":
                c.execute(
                    "UPDATE clips SET review = 'discarded', reviewed_by = ?, reviewed_at = ?"
                    " WHERE id = ?",
                    (user, now, clip_id),
                )
            elif action == "retry":
                c.execute(
                    "UPDATE clips SET status = 'pending', review = 'pending', error = NULL"
                    " WHERE id = ?",
                    (clip_id,),
                )
            else:
                raise ValueError(action)

    def usable_clips(self) -> list[dict]:
        return self.query(
            "SELECT * FROM clips WHERE status = 'processed' AND review IN ('auto', 'confirmed')"
            " AND label != 'discarded' ORDER BY received_at"
        )

    def clip_counts(self) -> list[dict]:
        return self.query(
            "SELECT label, review, status, COUNT(*) AS n FROM clips GROUP BY label, review, status"
        )

    # ----------------------------------------------------------------- runs

    def new_usable_counts(self) -> dict[str, int]:
        rows = self.query(
            "SELECT label, COUNT(*) AS n FROM clips WHERE status = 'processed'"
            " AND review IN ('auto', 'confirmed') AND label != 'discarded'"
            " AND first_run_id IS NULL GROUP BY label"
        )
        return {r["label"]: r["n"] for r in rows}

    def start_run(self, settings, forced: bool, now: float) -> tuple[dict | None, dict]:
        """Run the gate and, if it passes, record a new running run.

        Returns (run or None, gate). The gate and the insert share one
        transaction, so two trainers cannot both start.
        """
        with self.tx() as c:
            c.execute(
                "UPDATE runs SET status = 'failed', finished_at = ?, error = 'abandoned:"
                " no result within the run timeout' WHERE status = 'running' AND started_at < ?",
                (now, now - settings.run_timeout_hours * 3600),
            )
            running = c.execute(
                "SELECT id FROM runs WHERE status = 'running'"
            ).fetchone()
            last = c.execute("SELECT MAX(started_at) AS t FROM runs").fetchone()["t"]
            counts = self.new_usable_counts()
            positives = sum(counts.get(lbl, 0) for lbl in POSITIVE_LABELS)
            false_accepts = counts.get("false_accept", 0)
            gate = {
                "checked_at": now,
                "forced": forced,
                "new_positives": positives,
                "new_false_accepts": false_accepts,
                "new_true_rejects": counts.get("true_reject", 0),
            }
            if running:
                gate.update(start=False, reason=f"run {running['id']} is still running")
            elif forced:
                gate.update(start=True, reason="started by hand")
            elif last and now - last < settings.gate_min_hours_between_runs * 3600:
                gate.update(start=False, reason="a run already started in the last day")
            elif positives >= settings.gate_min_positives:
                gate.update(start=True, reason=f"{positives} new positives")
            elif false_accepts >= settings.gate_min_false_accepts:
                gate.update(start=True, reason=f"{false_accepts} new false accepts")
            else:
                gate.update(
                    start=False,
                    reason=(
                        f"{positives}/{settings.gate_min_positives} new positives,"
                        f" {false_accepts}/{settings.gate_min_false_accepts} new false accepts"
                    ),
                )
            c.execute(
                "INSERT INTO meta (key, value) VALUES ('last_gate', ?)"
                " ON CONFLICT (key) DO UPDATE SET value = excluded.value",
                (json.dumps(gate),),
            )
            if not gate["start"]:
                return None, gate
            run_id = (
                time.strftime("%Y%m%d-%H%M%S", time.gmtime(now))
                + "-"
                + uuid.uuid4().hex[:6]
            )
            c.execute(
                "INSERT INTO runs (id, started_at, status, forced, gate) VALUES (?, ?, 'running', ?, ?)",
                (run_id, now, int(forced), json.dumps(gate)),
            )
        return self.run(run_id), gate

    def run(self, run_id: str) -> dict | None:
        return self.one("SELECT * FROM runs WHERE id = ?", run_id)

    def runs(self) -> list[dict]:
        return self.query("SELECT * FROM runs ORDER BY started_at")

    def finish_run(
        self, run_id: str, status: str, now: float, result=None, error=None, clip_ids=()
    ):
        with self.tx() as c:
            promoted = None if result is None else int(bool(result.get("promote")))
            c.execute(
                "UPDATE runs SET status = ?, finished_at = ?, result = ?, error = ?, promoted = ?"
                " WHERE id = ?",
                (
                    status,
                    now,
                    None if result is None else json.dumps(result),
                    error,
                    promoted,
                    run_id,
                ),
            )
            if status == "succeeded":
                for i in clip_ids:
                    c.execute(
                        "UPDATE clips SET first_run_id = ? WHERE id = ? AND first_run_id IS NULL",
                        (run_id, i),
                    )

    def set_published(self, run_id: str, published: dict):
        with self.tx() as c:
            c.execute(
                "UPDATE runs SET published = ? WHERE id = ?",
                (json.dumps(published), run_id),
            )

    def meta(self, key: str):
        r = self.one("SELECT value FROM meta WHERE key = ?", key)
        return json.loads(r["value"]) if r else None
