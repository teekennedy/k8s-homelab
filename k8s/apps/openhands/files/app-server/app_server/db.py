"""SQLite persistence. One writer (this process), so a lock and a single
connection are the whole concurrency story."""

import sqlite3
import threading
from collections.abc import Iterator
from contextlib import contextmanager
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

# Append-only: each entry runs once, in order, and PRAGMA user_version records
# how many have run. Never edit one that has shipped.
MIGRATIONS: list[str] = [
    """
    CREATE TABLE sandboxes (
        id TEXT PRIMARY KEY,
        spec TEXT NOT NULL,
        session_api_key TEXT NOT NULL,
        created_by TEXT NOT NULL,
        created_at TEXT NOT NULL,
        last_active_at TEXT NOT NULL,
        -- The Sandbox's metadata.uid, once it exists.
        uid TEXT,
        deleted_at TEXT
    );
    CREATE TABLE api_keys (
        key_hash TEXT PRIMARY KEY,
        name TEXT NOT NULL,
        created_at TEXT NOT NULL
    );
    """,
    """
    CREATE TABLE conversations (
        id TEXT PRIMARY KEY,
        sandbox_id TEXT NOT NULL,
        title TEXT,
        created_by TEXT NOT NULL,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        -- Everything the frontend's AppConversation carries that is not
        -- derived: request metadata, and what the agent server last reported.
        meta TEXT NOT NULL,
        deleted_at TEXT
    );
    CREATE INDEX conversations_by_updated ON conversations (updated_at);
    CREATE TABLE start_tasks (
        id TEXT PRIMARY KEY,
        status TEXT NOT NULL,
        detail TEXT,
        app_conversation_id TEXT,
        sandbox_id TEXT,
        request TEXT NOT NULL,
        created_by TEXT NOT NULL,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
    );
    -- History, as the sandbox's webhooks deliver it. Events are immutable and
    -- redelivered on retry, hence the (conversation, id) key.
    CREATE TABLE events (
        conversation_id TEXT NOT NULL,
        id TEXT NOT NULL,
        timestamp TEXT NOT NULL,
        kind TEXT NOT NULL,
        body TEXT NOT NULL,
        PRIMARY KEY (conversation_id, id)
    );
    CREATE INDEX events_by_time ON events (conversation_id, timestamp, id);
    -- Single-document stores: settings, agent profiles. One user.
    CREATE TABLE documents (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL
    );
    """,
    """
    -- User-added conversation secrets, encrypted with SECRETS_KEY.
    CREATE TABLE secrets (
        name TEXT PRIMARY KEY,
        value BLOB NOT NULL,
        description TEXT,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL
    );
    """,
    """
    -- Who asked for the sandbox: a browser ('user') or a service caller such
    -- as an automation run ('service'). They are collected differently.
    ALTER TABLE sandboxes ADD COLUMN owner_kind TEXT NOT NULL DEFAULT 'user';
    -- When it was last suspended, while it still is.
    ALTER TABLE sandboxes ADD COLUMN suspended_at TEXT;
    """,
]


def now() -> str:
    return datetime.now(UTC).isoformat()


class Database:
    def __init__(self, path: Path | str):
        self._conn = sqlite3.connect(str(path), check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._lock = threading.Lock()
        with self._lock:
            self._conn.execute("PRAGMA journal_mode=WAL")
            self._migrate()

    def _migrate(self) -> None:
        version = self._conn.execute("PRAGMA user_version").fetchone()[0]
        for i, script in enumerate(MIGRATIONS[version:], start=version + 1):
            self._conn.executescript(
                f"BEGIN; {script}; PRAGMA user_version = {i}; COMMIT;"
            )

    @contextmanager
    def tx(self) -> Iterator[sqlite3.Connection]:
        with self._lock:
            try:
                yield self._conn
                self._conn.commit()
            except BaseException:
                self._conn.rollback()
                raise

    def one(self, sql: str, *args: Any) -> dict[str, Any] | None:
        with self.tx() as c:
            row = c.execute(sql, args).fetchone()
        return dict(row) if row else None

    def all(self, sql: str, *args: Any) -> list[dict[str, Any]]:
        with self.tx() as c:
            return [dict(r) for r in c.execute(sql, args).fetchall()]

    def run(self, sql: str, *args: Any) -> int:
        with self.tx() as c:
            return c.execute(sql, args).rowcount

    def close(self) -> None:
        self._conn.close()
