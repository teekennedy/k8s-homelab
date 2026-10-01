"""Conversation secrets the user adds in Settings → Secrets.

Each is handed to every new conversation as a `StaticSecret`, which the agent
exposes under its name — so a secret named like an environment variable is
how a credential reaches an agent without being in the pod spec. Values are
encrypted at rest and never returned.

Wire shapes: `CloudClient` (`/api/v1/secrets/search` pages of
`{name, description}`, POST `{name, value, description}`, PUT `/{name}`
`{name, description}` to rename or re-describe).
"""

import base64
import hashlib
import re
from typing import Any

from cryptography.fernet import Fernet

from .db import Database, now

# The agent server's own rule for secret names: environment-variable shaped.
NAME = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")


class SecretError(Exception):
    def __init__(self, status: int, detail: str):
        super().__init__(detail)
        self.status = status
        self.detail = detail


class SecretsStore:
    def __init__(self, db: Database, key: str):
        self.db = db
        digest = hashlib.sha256(key.encode()).digest()
        self._fernet = Fernet(base64.urlsafe_b64encode(digest))

    @staticmethod
    def _check_name(name: str) -> None:
        if not NAME.match(name):
            raise SecretError(
                422, "secret names are letters, digits and _, not starting with a digit"
            )

    def page(self, limit: int, page_id: str | None) -> dict[str, Any]:
        offset = int(page_id or 0)
        rows = self.db.all(
            "SELECT name, description FROM secrets ORDER BY name LIMIT ? OFFSET ?",
            limit + 1,
            offset,
        )
        return {
            "items": rows[:limit],
            "next_page_id": str(offset + limit) if len(rows) > limit else None,
        }

    def create(self, name: str, value: str, description: str | None) -> None:
        self._check_name(name)
        if self.db.one("SELECT 1 FROM secrets WHERE name = ?", name):
            raise SecretError(409, f"secret {name!r} already exists")
        ts = now()
        self.db.run(
            "INSERT INTO secrets (name, value, description, created_at, updated_at)"
            " VALUES (?, ?, ?, ?, ?)",
            name,
            self._fernet.encrypt(value.encode()),
            description,
            ts,
            ts,
        )

    def update(self, name: str, new_name: str | None, description: str | None) -> None:
        if not self.db.one("SELECT 1 FROM secrets WHERE name = ?", name):
            raise SecretError(404, f"secret {name!r} not found")
        new_name = new_name or name
        self._check_name(new_name)
        if new_name != name and self.db.one(
            "SELECT 1 FROM secrets WHERE name = ?", new_name
        ):
            raise SecretError(409, f"secret {new_name!r} already exists")
        self.db.run(
            "UPDATE secrets SET name = ?, description = ?, updated_at = ?"
            " WHERE name = ?",
            new_name,
            description,
            now(),
            name,
        )

    def delete(self, name: str) -> None:
        if not self.db.run("DELETE FROM secrets WHERE name = ?", name):
            raise SecretError(404, f"secret {name!r} not found")

    def for_conversation(self) -> dict[str, dict[str, Any]]:
        """`StartConversationRequest.secrets`: every stored secret."""
        return {
            r["name"]: {
                "kind": "StaticSecret",
                "value": self._fernet.decrypt(r["value"]).decode(),
                "description": r["description"],
            }
            for r in self.db.all("SELECT name, value, description FROM secrets")
        }
