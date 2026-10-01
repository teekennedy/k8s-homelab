"""Sandbox lifecycle: one agents.x-k8s.io Sandbox per sandbox id.

A Sandbox owns its pod, its headless Service and its workspace PVC; the
per-sandbox config Secret is made a dependent of it too, so deleting the
Sandbox is the whole cleanup.
"""

import json
import logging
import secrets
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal

from .db import Database, now
from .kube import Kube, Resource

log = logging.getLogger(__name__)

PLACEHOLDER = "__SANDBOX_ID__"
MANAGED_BY = "openhands.msng.to/managed-by"
MANAGED_BY_VALUE = "openhands-app-server"
SANDBOX_ID_LABEL = "openhands.msng.to/sandbox-id"

Status = Literal["STARTING", "RUNNING", "PAUSED", "ERROR", "MISSING"]


class UnknownSpec(ValueError):
    pass


def load_specs(specs_dir: Path) -> dict[str, dict[str, Any]]:
    """One JSON file per spec, rendered by Helm: a Sandbox manifest with
    PLACEHOLDER wherever the sandbox id belongs."""
    return {p.stem: json.loads(p.read_text()) for p in sorted(specs_dir.glob("*.json"))}


def render(template: dict[str, Any], sandbox_id: str) -> dict[str, Any]:
    manifest = json.loads(json.dumps(template).replace(PLACEHOLDER, sandbox_id))
    meta = manifest.setdefault("metadata", {})
    meta["name"] = sandbox_id
    labels = meta.setdefault("labels", {})
    labels[MANAGED_BY] = MANAGED_BY_VALUE
    labels[SANDBOX_ID_LABEL] = sandbox_id
    return manifest


def derive_status(cr: dict[str, Any] | None, pod: dict[str, Any] | None) -> Status:
    if cr is None:
        return "MISSING"
    # restartPolicy is Never: an agent server that exited stays exited.
    if pod and pod.get("status", {}).get("phase") in ("Failed", "Succeeded"):
        return "ERROR"
    if cr.get("spec", {}).get("operatingMode") == "Suspended":
        return "PAUSED"
    conditions = {
        c["type"]: c.get("status") for c in cr.get("status", {}).get("conditions", [])
    }
    if conditions.get("Ready") == "True":
        return "RUNNING"
    return "STARTING"


class SandboxManager:
    def __init__(
        self,
        db: Database,
        kube: Kube,
        namespace: str,
        specs: dict[str, dict[str, Any]],
        webhook_url: str,
        agent_server_port: int,
    ):
        self.db = db
        self.kube = kube
        self.namespace = namespace
        self.specs = specs
        self.webhook_url = webhook_url
        self.port = agent_server_port
        self.sandboxes = Resource("agents.x-k8s.io", "v1beta1", "sandboxes", namespace)
        self.secrets = Resource("", "v1", "secrets", namespace)
        self.pods = Resource("", "v1", "pods", namespace)

    # --- rows ---------------------------------------------------------------

    def row(self, sandbox_id: str) -> dict[str, Any] | None:
        return self.db.one("SELECT * FROM sandboxes WHERE id = ?", sandbox_id)

    def live_row(self, sandbox_id: str) -> dict[str, Any] | None:
        return self.db.one(
            "SELECT * FROM sandboxes WHERE id = ? AND deleted_at IS NULL", sandbox_id
        )

    def touch(self, sandbox_id: str) -> None:
        self.db.run(
            "UPDATE sandboxes SET last_active_at = ? WHERE id = ?", now(), sandbox_id
        )

    def agent_url(self, sandbox_id: str) -> str:
        """In-cluster address of a sandbox's agent server, via its headless Service."""
        return f"http://{sandbox_id}.{self.namespace}.svc.cluster.local:{self.port}"

    # --- lifecycle ----------------------------------------------------------

    def agent_server_config(self, sandbox_id: str) -> dict[str, Any]:
        return {
            "webhooks": [
                {
                    "base_url": f"{self.webhook_url}/sandboxes/{sandbox_id}",
                    "event_buffer_size": 10,
                    "flush_delay": 2.0,
                }
            ],
            "conversations_path": "/workspace/conversations",
            "workspace_path": "/workspace/project",
            "bash_events_dir": "/workspace/bash_events",
            # The editor listens on a second port the runtime proxy never reaches.
            "enable_vscode": False,
        }

    async def create(self, spec_name: str, created_by: str) -> dict[str, Any]:
        template = self.specs.get(spec_name)
        if template is None:
            raise UnknownSpec(spec_name)
        sandbox_id = f"sbx-{secrets.token_hex(6)}"
        session_api_key = secrets.token_urlsafe(32)
        ts = now()
        # The row first: the reconciler deletes any managed Sandbox without one.
        self.db.run(
            "INSERT INTO sandboxes (id, spec, session_api_key, created_by, created_at,"
            " last_active_at) VALUES (?, ?, ?, ?, ?, ?)",
            sandbox_id,
            spec_name,
            session_api_key,
            created_by,
            ts,
            ts,
        )
        try:
            sandbox = await self.kube.create(
                self.sandboxes, render(template, sandbox_id)
            )
            await self.kube.create(
                self.secrets,
                {
                    "apiVersion": "v1",
                    "kind": "Secret",
                    "metadata": {
                        "name": f"{sandbox_id}-config",
                        "labels": {
                            MANAGED_BY: MANAGED_BY_VALUE,
                            SANDBOX_ID_LABEL: sandbox_id,
                        },
                        # Not blockOwnerDeletion: that needs update on
                        # sandboxes/finalizers, which this server is not granted.
                        "ownerReferences": [
                            {
                                "apiVersion": "agents.x-k8s.io/v1beta1",
                                "kind": "Sandbox",
                                "name": sandbox_id,
                                "uid": sandbox["metadata"]["uid"],
                            }
                        ],
                    },
                    "type": "Opaque",
                    "stringData": {
                        "session-api-key": session_api_key,
                        "secret-key": secrets.token_urlsafe(32),
                        "config.json": json.dumps(self.agent_server_config(sandbox_id)),
                    },
                },
            )
        except Exception:
            log.exception("creating sandbox %s failed; rolling back", sandbox_id)
            await self.kube.delete(self.sandboxes, sandbox_id)
            self.db.run(
                "UPDATE sandboxes SET deleted_at = ? WHERE id = ?", now(), sandbox_id
            )
            raise
        log.info("created sandbox %s (spec %s)", sandbox_id, spec_name)
        return self.row(sandbox_id)

    async def status(self, sandbox_id: str) -> Status:
        cr = await self.kube.get(self.sandboxes, sandbox_id)
        pod = await self.kube.get(self.pods, sandbox_id) if cr else None
        return derive_status(cr, pod)

    async def set_mode(self, sandbox_id: str, mode: str) -> bool:
        if not self.live_row(sandbox_id):
            return False
        if await self.kube.get(self.sandboxes, sandbox_id) is None:
            return False
        await self.kube.patch(
            self.sandboxes, sandbox_id, {"spec": {"operatingMode": mode}}
        )
        self.touch(sandbox_id)
        log.info("sandbox %s -> %s", sandbox_id, mode)
        return True

    async def pause(self, sandbox_id: str) -> bool:
        return await self.set_mode(sandbox_id, "Suspended")

    async def resume(self, sandbox_id: str) -> bool:
        return await self.set_mode(sandbox_id, "Running")

    async def delete(self, sandbox_id: str) -> bool:
        if not self.live_row(sandbox_id):
            return False
        await self.kube.delete(self.sandboxes, sandbox_id)
        self.db.run(
            "UPDATE sandboxes SET deleted_at = ? WHERE id = ?", now(), sandbox_id
        )
        log.info("deleted sandbox %s", sandbox_id)
        return True

    # --- views --------------------------------------------------------------

    async def info(self, row: dict[str, Any], base_url: str) -> dict[str, Any]:
        """V1SandboxInfo, as the frontend and the automation service read it."""
        sandbox_id = row["id"]
        status: Status = (
            "MISSING" if row["deleted_at"] else await self.status(sandbox_id)
        )
        alive = status in ("RUNNING", "STARTING", "PAUSED")
        return {
            "id": sandbox_id,
            "created_by_user_id": row["created_by"],
            "sandbox_spec_id": row["spec"],
            "status": status,
            "session_api_key": row["session_api_key"] if alive else None,
            "exposed_urls": (
                [{"name": "AGENT_SERVER", "url": f"{base_url}/runtime/{sandbox_id}"}]
                if status == "RUNNING"
                else None
            ),
            "created_at": row["created_at"],
        }

    def page(self, limit: int, page_id: str | None) -> tuple[list[dict], str | None]:
        offset = int(page_id or 0)
        rows = self.db.all(
            "SELECT * FROM sandboxes WHERE deleted_at IS NULL"
            " ORDER BY created_at DESC LIMIT ? OFFSET ?",
            limit + 1,
            offset,
        )
        next_page = str(offset + limit) if len(rows) > limit else None
        return rows[:limit], next_page

    # --- reconciliation -----------------------------------------------------

    async def reconcile(self, grace_seconds: float = 120.0) -> None:
        """Make the rows and the cluster agree.

        A managed Sandbox with no live row is deleted (its row was deleted, or
        a create crashed between the two writes). A live row whose Sandbox is
        gone — deleted by hand, or by a namespace wipe — is marked deleted, so
        it reports MISSING rather than STARTING forever.

        Anything younger than the grace period is left alone: a create writes
        the row and the Sandbox moments apart, and this reads them moments
        apart, so a fresh one can look like either case.
        """
        cutoff = datetime.now(UTC) - timedelta(seconds=grace_seconds)
        crs = await self.kube.list(self.sandboxes, f"{MANAGED_BY}={MANAGED_BY_VALUE}")
        rows = self.db.all(
            "SELECT id, created_at FROM sandboxes WHERE deleted_at IS NULL"
        )
        in_cluster = {cr["metadata"]["name"] for cr in crs}
        live = {r["id"] for r in rows}
        for cr in crs:
            name = cr["metadata"]["name"]
            created = datetime.fromisoformat(cr["metadata"]["creationTimestamp"])
            if name not in live and created < cutoff:
                log.warning("reconcile: deleting sandbox %s with no live row", name)
                await self.kube.delete(self.sandboxes, name)
        for r in rows:
            created = datetime.fromisoformat(r["created_at"])
            if r["id"] not in in_cluster and created < cutoff:
                log.warning("reconcile: sandbox %s is gone from the cluster", r["id"])
                self.db.run(
                    "UPDATE sandboxes SET deleted_at = ? WHERE id = ?", now(), r["id"]
                )
