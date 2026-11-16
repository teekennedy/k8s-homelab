"""Alertmanager as an event source for automations, on a port of its own.

The automation service verifies a custom source by an HMAC of the body, which
Alertmanager cannot compute; it can only present a bearer token. So
Alertmanager posts here, and this server forwards the delivery signed with a
secret derived from its own automation key, after two cuts:

- Firing alerts only. A resolution is not something to investigate.
- An alert name forwarded within the cooldown is dropped. That covers every
  replica of an HA Alertmanager delivering the same group, a group notified
  again as more instances join it, and a long-firing alert's repeats.

Grouping alerts that do get through into one run is the automation's trigger,
not this module: see README.md, "Alert triage".
"""

import hashlib
import hmac
import json
import logging
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import httpx
from fastapi import APIRouter, Body, HTTPException, Request

from .db import Database
from .routes_account import USER_ID

log = logging.getLogger(__name__)
router = APIRouter()

SIGNATURE_HEADER = "X-OpenHands-Signature"
# Alerts rendered into one event. A storm of one alert name is one group and
# can carry hundreds of instances; the first few say what the rest do.
MAX_ALERTS = 25
_KEPT_FIELDS = ("labels", "annotations", "startsAt", "generatorURL", "fingerprint")


def source_secret(automation_api_key: str, source: str) -> str:
    """The HMAC secret for a source this server signs for. Derived, so there
    is nothing to store, and rotated along with the automation key."""
    return hmac.new(
        automation_api_key.encode(), f"event-source:{source}".encode(), hashlib.sha256
    ).hexdigest()


def sign(body: bytes, secret: str) -> str:
    return hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()


def _name(alert: dict[str, Any]) -> str:
    return (alert.get("labels") or {}).get("alertname") or "unnamed"


def render(alerts: list[dict[str, Any]]) -> str:
    """The alerts as text: the whole of what a running conversation is told
    about a follow-up event."""
    lines = [f"{len(alerts)} alert(s) firing:"]
    for alert in alerts[:MAX_ALERTS]:
        labels = alert.get("labels") or {}
        annotations = alert.get("annotations") or {}
        rest = ", ".join(
            f"{k}={v}" for k, v in sorted(labels.items()) if k != "alertname"
        )
        lines.append(f"- {_name(alert)} {{{rest}}} since {alert.get('startsAt')}")
        text = (
            annotations.get("summary")
            or annotations.get("description")
            or annotations.get("message")
        )
        if text:
            lines.append(f"  {text.strip()}")
    if len(alerts) > MAX_ALERTS:
        lines.append(f"- ... and {len(alerts) - MAX_ALERTS} more")
    return "\n".join(lines)


class AlertIntake:
    def __init__(
        self,
        db: Database,
        http: httpx.AsyncClient,
        *,
        automation_url: str,
        automation_api_key: str,
        source: str,
        token_file: Path | None,
        cooldown: float,
    ):
        self.db = db
        self.http = http
        self.url = f"{automation_url}/api/automation/v1/events/{USER_ID}/{source}"
        self.secret = source_secret(automation_api_key, source)
        self.token_file = token_file
        self.cooldown = cooldown

    def authenticate(self, authorization: str | None) -> None:
        token = ""
        if self.token_file and self.token_file.exists():
            token = self.token_file.read_text().strip()
        if not token:
            # The Secret is mounted optional; never accept an empty token.
            raise HTTPException(503, "no token configured")
        expected = f"Bearer {token}".encode()
        if not hmac.compare_digest(expected, (authorization or "").encode()):
            raise HTTPException(401, "not authenticated")

    def _claim(self, names: set[str]) -> tuple[set[str], str]:
        """The names not forwarded within the cooldown, marked as forwarded
        now. Checked and marked in one transaction, so two replicas
        delivering the same group cannot both get through."""
        stamp = datetime.now(UTC).isoformat()
        if self.cooldown <= 0:
            return names, stamp
        cutoff = (datetime.now(UTC) - timedelta(seconds=self.cooldown)).isoformat()
        with self.db.tx() as c:
            recent = {
                r[0]
                for r in c.execute(
                    "SELECT alertname FROM alert_cooldowns WHERE forwarded_at > ?",
                    (cutoff,),
                )
            }
            fresh = names - recent
            c.executemany(
                "INSERT INTO alert_cooldowns (alertname, forwarded_at) VALUES (?, ?)"
                " ON CONFLICT (alertname) DO UPDATE SET forwarded_at = excluded.forwarded_at",
                [(n, stamp) for n in fresh],
            )
        return fresh, stamp

    def _release(self, names: set[str], stamp: str) -> None:
        with self.db.tx() as c:
            c.executemany(
                "DELETE FROM alert_cooldowns WHERE alertname = ? AND forwarded_at = ?",
                [(n, stamp) for n in names],
            )

    async def forward(self, notification: dict[str, Any]) -> dict[str, Any]:
        firing = [
            a for a in notification.get("alerts") or [] if a.get("status") == "firing"
        ]
        if not firing:
            return {"forwarded": 0}
        fresh, stamp = self._claim({_name(a) for a in firing})
        alerts = [a for a in firing if _name(a) in fresh]
        if not alerts:
            log.info(
                "alerts cooling down, not forwarded: %s",
                ", ".join(sorted({_name(a) for a in firing})),
            )
            return {"forwarded": 0}
        event = {
            "status": "firing",
            "summary": render(alerts),
            "alert_count": len(alerts),
            "alerts": [
                {k: a.get(k) for k in _KEPT_FIELDS} for a in alerts[:MAX_ALERTS]
            ],
            "externalURL": notification.get("externalURL"),
        }
        body = json.dumps(event).encode()
        try:
            resp = await self.http.post(
                self.url,
                content=body,
                headers={
                    "Content-Type": "application/json",
                    SIGNATURE_HEADER: sign(body, self.secret),
                },
                timeout=30,
            )
            if resp.status_code >= 400:
                raise RuntimeError(f"{resp.status_code}: {resp.text[:300]}")
        except Exception as e:
            # Not forwarded, so not cooling down either; Alertmanager retries.
            self._release(fresh, stamp)
            log.warning("could not forward alerts: %s", e)
            raise HTTPException(502, "the automation service did not take the event")
        log.info("forwarded alerts: %s", ", ".join(sorted(fresh)))
        return {"forwarded": len(alerts)}


@router.post("/alertmanager")
async def alertmanager(
    request: Request, notification: dict[str, Any] = Body()
) -> dict[str, Any]:
    """Alertmanager's webhook_config payload (version 4)."""
    intake: AlertIntake = request.app.state.alerts
    intake.authenticate(request.headers.get("Authorization"))
    return await intake.forward(notification)
