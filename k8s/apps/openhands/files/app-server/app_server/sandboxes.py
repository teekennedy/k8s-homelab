"""Sandbox lifecycle: one agents.x-k8s.io Sandbox per sandbox id.

A Sandbox owns its pod, its headless Service and its workspace PVC; the
per-sandbox config Secret is made a dependent of it too, so deleting the
Sandbox is the whole cleanup.
"""

import asyncio
import hmac
import json
import logging
import secrets
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any, Literal

from .db import Database, now
from .kube import Kube, KubeError, Resource

log = logging.getLogger(__name__)

PLACEHOLDER = "__SANDBOX_ID__"
MANAGED_BY = "openhands.msng.to/managed-by"
MANAGED_BY_VALUE = "openhands-app-server"
SANDBOX_ID_LABEL = "openhands.msng.to/sandbox-id"
# On every sandbox pod, from the chart's runtimeSelectorLabels.
RUNTIME_POD_SELECTOR = "app.kubernetes.io/component=runtime"
TOUCH_EVERY = timedelta(minutes=1)

Status = Literal["STARTING", "RUNNING", "PAUSED", "ERROR", "MISSING"]


class UnknownSpec(ValueError):
    pass


class AtCapacity(Exception):
    pass


@dataclass(frozen=True)
class Limits:
    """What the collector enforces, in seconds; 0 switches a limit off."""

    # Sandboxes that may be running at once. Suspended ones do not count.
    max_running: int = 0
    # A browser's sandbox with no activity and no running agent is suspended:
    # the pod goes, the volume stays, and opening the conversation resumes it.
    idle_suspend: float = 0
    # A sandbox left suspended this long is deleted, volume and all.
    suspended_delete: float = 0
    # A service caller's sandbox that never got a conversation: a run that
    # failed while the sandbox was still starting never releases it.
    service_orphan: float = 0
    # A service caller's sandbox, however busy: no run lasts this long.
    service_max: float = 0


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
        limits: Limits = Limits(),
    ):
        self.db = db
        self.kube = kube
        self.namespace = namespace
        self.specs = specs
        self.webhook_url = webhook_url
        self.port = agent_server_port
        self.limits = limits
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

    def check_session_key(self, sandbox_id: str, given: str | None) -> bool:
        """Whether `given` is this live sandbox's own session key."""
        row = self.live_row(sandbox_id)
        return (
            row is not None
            and bool(given)
            and hmac.compare_digest(given, row["session_api_key"])
        )

    def touch(self, sandbox_id: str) -> None:
        """Record activity, at most once a minute: called on every proxied
        request, and a no-op UPDATE is cheaper than remembering when."""
        ts = datetime.now(UTC)
        self.db.run(
            "UPDATE sandboxes SET last_active_at = ? WHERE id = ? AND last_active_at < ?",
            ts.isoformat(),
            sandbox_id,
            (ts - TOUCH_EVERY).isoformat(),
        )

    def _mark_deleted(self, sandbox_id: str) -> None:
        self.db.run(
            "UPDATE sandboxes SET deleted_at = ? WHERE id = ?", now(), sandbox_id
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

    async def create(
        self, spec_name: str, created_by: str, owner_kind: str = "user"
    ) -> dict[str, Any]:
        template = self.specs.get(spec_name)
        if template is None:
            raise UnknownSpec(spec_name)
        running = self.db.one(
            "SELECT COUNT(*) AS n FROM sandboxes"
            " WHERE deleted_at IS NULL AND suspended_at IS NULL"
        )["n"]
        if self.limits.max_running and running >= self.limits.max_running:
            raise AtCapacity(
                f"{running} sandboxes are running, which is the limit; stop or"
                " delete a conversation, or wait for one to be suspended"
            )
        sandbox_id = f"sbx-{secrets.token_hex(6)}"
        session_api_key = secrets.token_urlsafe(32)
        ts = now()
        # The row first: the reconciler deletes any managed Sandbox without one.
        self.db.run(
            "INSERT INTO sandboxes (id, spec, session_api_key, created_by, created_at,"
            " last_active_at, owner_kind) VALUES (?, ?, ?, ?, ?, ?, ?)",
            sandbox_id,
            spec_name,
            session_api_key,
            created_by,
            ts,
            ts,
            owner_kind,
        )
        try:
            sandbox = await self.kube.create(
                self.sandboxes, render(template, sandbox_id)
            )
            uid = sandbox["metadata"]["uid"]
            # From here the reconciler may treat a missing Sandbox as vanished.
            self.db.run("UPDATE sandboxes SET uid = ? WHERE id = ?", uid, sandbox_id)
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
                                "uid": uid,
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
            self._mark_deleted(sandbox_id)
            raise
        log.info("created sandbox %s (spec %s)", sandbox_id, spec_name)
        return self.row(sandbox_id)

    async def set_mode(self, sandbox_id: str, mode: str) -> bool:
        if not self.live_row(sandbox_id):
            return False
        try:
            await self.kube.patch(
                self.sandboxes, sandbox_id, {"spec": {"operatingMode": mode}}
            )
        except KubeError as e:
            if e.status == 404:
                return False
            raise
        self.db.run(
            "UPDATE sandboxes SET suspended_at = ?, last_active_at = ? WHERE id = ?",
            now() if mode == "Suspended" else None,
            now(),
            sandbox_id,
        )
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
        self._mark_deleted(sandbox_id)
        log.info("deleted sandbox %s", sandbox_id)
        return True

    # --- views --------------------------------------------------------------

    async def statuses(self) -> dict[str, Status]:
        """Status of every managed Sandbox in the cluster, from two list calls
        whatever the count. Ids absent from the result are MISSING."""
        crs, pods = await asyncio.gather(
            self.kube.list(self.sandboxes, f"{MANAGED_BY}={MANAGED_BY_VALUE}"),
            self.kube.list(self.pods, RUNTIME_POD_SELECTOR),
        )
        by_name = {p["metadata"]["name"]: p for p in pods}
        return {
            cr["metadata"]["name"]: derive_status(
                cr, by_name.get(cr["metadata"]["name"])
            )
            for cr in crs
        }

    def info(
        self, row: dict[str, Any], status: Status, base_url: str
    ) -> dict[str, Any]:
        """V1SandboxInfo, as the frontend and the automation service read it."""
        sandbox_id = row["id"]
        if row["deleted_at"]:
            status = "MISSING"
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

    async def reconcile(self) -> None:
        """Make the rows and the cluster agree.

        A managed Sandbox with no live row is deleted (its row was deleted, or
        a create crashed between the two writes). A live row whose Sandbox is
        gone — deleted by hand, or by a namespace wipe — is marked deleted, so
        it reports MISSING rather than STARTING forever.

        Read order makes this safe against a concurrent create, which writes
        the row, then the Sandbox, then the row's uid. A row that had its uid
        before the list had its Sandbox before the list too; a Sandbox in the
        list had its row before the second read.
        """
        created = {
            r["id"]
            for r in self.db.all(
                "SELECT id FROM sandboxes WHERE deleted_at IS NULL AND uid IS NOT NULL"
            )
        }
        crs = await self.kube.list(self.sandboxes, f"{MANAGED_BY}={MANAGED_BY_VALUE}")
        in_cluster = {cr["metadata"]["name"] for cr in crs}
        live = {
            r["id"]
            for r in self.db.all("SELECT id FROM sandboxes WHERE deleted_at IS NULL")
        }
        for orphan in sorted(in_cluster - live):
            log.warning("reconcile: deleting sandbox %s with no live row", orphan)
            await self.kube.delete(self.sandboxes, orphan)
        for vanished in sorted(created - in_cluster):
            log.warning("reconcile: sandbox %s is gone from the cluster", vanished)
            self._mark_deleted(vanished)

    # --- collection ---------------------------------------------------------

    async def collect(self, busy: set[str], claimed: set[str]) -> None:
        """Apply the limits. `busy` are sandboxes whose agent is working or
        whose conversation is still starting; `claimed` are those that have a
        conversation at all."""
        limits = self.limits
        at = datetime.now(UTC)

        def older(ts: str | None, seconds: float) -> bool:
            return bool(seconds and ts) and (
                at - datetime.fromisoformat(ts) > timedelta(seconds=seconds)
            )

        for row in self.db.all("SELECT * FROM sandboxes WHERE deleted_at IS NULL"):
            sandbox_id = row["id"]
            if row["owner_kind"] == "service":
                orphan = sandbox_id not in claimed | busy and older(
                    row["created_at"], limits.service_orphan
                )
                if orphan or older(row["created_at"], limits.service_max):
                    log.warning(
                        "collect: deleting service sandbox %s (%s)",
                        sandbox_id,
                        "no conversation" if orphan else "too old",
                    )
                    await self.delete(sandbox_id)
            elif row["suspended_at"]:
                if older(row["suspended_at"], limits.suspended_delete):
                    log.warning("collect: deleting suspended sandbox %s", sandbox_id)
                    await self.delete(sandbox_id)
            elif sandbox_id not in busy and older(
                row["last_active_at"], limits.idle_suspend
            ):
                log.info("collect: suspending idle sandbox %s", sandbox_id)
                await self.pause(sandbox_id)
