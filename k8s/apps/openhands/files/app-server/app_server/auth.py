"""Who is calling.

Two callers, two mechanisms:

- A browser, through oauth2-proxy, which has already checked the Authelia
  session and sets the user header. Requiring it here is defence in depth; the
  NetworkPolicy is what stops anything else dialling this port.
- The automation service, with a bearer key it minted through the service
  endpoint (X-Service-API-Key). Keys are stored hashed.
"""

import hashlib
import hmac
import secrets
from dataclasses import dataclass
from typing import Literal

from fastapi import HTTPException, Request

from .db import Database, now


@dataclass(frozen=True)
class Principal:
    kind: Literal["user", "service"]
    # The identity oauth2-proxy asserted, or the name the key was minted under.
    name: str


def _hash(key: str) -> str:
    return hashlib.sha256(key.encode()).hexdigest()


def mint_api_key(db: Database, name: str) -> str:
    key = f"ohk_{secrets.token_urlsafe(32)}"
    db.run(
        "INSERT INTO api_keys (key_hash, name, created_at) VALUES (?, ?, ?)",
        _hash(key),
        name,
        now(),
    )
    return key


def check_service_key(request: Request, expected: str) -> None:
    given = request.headers.get("X-Service-API-Key", "")
    if not given or not hmac.compare_digest(given, expected):
        raise HTTPException(401, "invalid service key")


def principal(request: Request) -> Principal:
    """FastAPI dependency: the authenticated caller, or 401."""
    db: Database = request.app.state.db
    user_header: str = request.app.state.settings.user_header
    auth = request.headers.get("Authorization", "")
    if auth.startswith("Bearer "):
        row = db.one(
            "SELECT name FROM api_keys WHERE key_hash = ?",
            _hash(auth[len("Bearer ") :]),
        )
        if row is None:
            raise HTTPException(401, "invalid API key")
        return Principal("service", row["name"])
    user = request.headers.get(user_header)
    if user:
        return Principal("user", user)
    raise HTTPException(401, "not authenticated")
