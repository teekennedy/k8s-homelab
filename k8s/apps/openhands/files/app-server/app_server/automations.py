"""Declarative automations, run by the automation service in cloud mode.

The automation service owns scheduling, webhook matching and run bookkeeping,
and gives every run a sandbox of its own (`automation/backends/cloud.py`). Its
own run scripts build an agent from an LLM API key, which this deployment
does not have, so each automation here is a *custom* one
(`automation/schemas.py`, CreateAutomationRequest): the same small tarball,
whose entry point asks this server to start the conversation.

Definitions come from a file (values.yaml `automations`) and are pushed to the
automation service, along with the tarball: the service takes one from its own
upload store or from a public https URL, and this server is neither public nor
https. An automation is ours if it runs one of our uploads; anything else in
that service is left alone.
"""

import hashlib
import io
import json
import logging
import tarfile
from pathlib import Path
from typing import Any

import httpx

from .settings import Settings

log = logging.getLogger(__name__)

ENTRYPOINT = "python3 run.py"
RUN_SCRIPT = Path(__file__).with_name("automation_run.py")
PAGE = 100
# Uploads are named for their content, so a new run script is a new upload.
UPLOAD_PREFIX = "openhands-app-server-run-"


def build_tarball(files: dict[str, bytes]) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as tar:
        for name, data in files.items():
            info = tarfile.TarInfo(name=name)
            info.size = len(data)
            tar.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def _covers(have: dict[str, Any], want: dict[str, Any]) -> bool:
    """Whether a stored trigger already says everything the definition does;
    the service fills in defaults the definition leaves out."""
    return all(have.get(k) == v for k, v in want.items())


class Automations:
    def __init__(self, http: httpx.AsyncClient, settings: Settings):
        self.http = http
        self.file = settings.automations_file
        self.api = f"{settings.automation_url}/api/automation/v1"
        self.headers = {"X-Session-API-Key": settings.automation_api_key}
        self.source = settings.forge_webhook_source
        self.secret_file = settings.forge_webhook_secret_file
        self.tarball = build_tarball(
            {
                "run.py": RUN_SCRIPT.read_bytes(),
                "run.json": json.dumps(
                    {
                        "webhook_url": settings.webhook_url,
                        "agent_server_port": settings.agent_server_port,
                    }
                ).encode(),
            }
        )
        digest = hashlib.sha256(self.tarball).hexdigest()[:16]
        self.upload_name = f"{UPLOAD_PREFIX}{digest}"

    def definitions(self) -> dict[str, dict[str, Any]]:
        return json.loads(self.file.read_text()) or {}

    def prompt(self, name: str | None, event: Any, follow_ups: Any) -> str | None:
        """The first message of a run, or None for an automation not defined
        here. An event-triggered run is told what triggered it."""
        definition = self.definitions().get(name or "")
        if definition is None:
            return None
        parts = [definition["prompt"].strip()]
        if event:
            parts.append(
                "## Event payload\n\nThis run was triggered by a webhook event:\n\n"
                f"```json\n{json.dumps(event, indent=2)}\n```"
            )
        if follow_ups:
            parts.append(
                "## Follow-up messages\n\nMore activity arrived on the same subject"
                " while this run was queued:\n\n"
                + "\n\n".join(str(turn) for turn in follow_ups)
            )
        return "\n\n".join(parts)

    async def _call(self, method: str, path: str = "", **kw: Any) -> Any:
        kw.setdefault("headers", self.headers)
        resp = await self.http.request(method, f"{self.api}{path}", timeout=30, **kw)
        if resp.status_code >= 400:
            # The service says which field it objected to in the body.
            raise RuntimeError(
                f"{method} {path or '/'} -> {resp.status_code}: {resp.text[:300]}"
            )
        return resp.json() if resp.content else None

    async def _all(self, path: str, key: str, **params: Any) -> list[dict[str, Any]]:
        found: list[dict[str, Any]] = []
        while True:
            page = await self._call(
                "GET", path, params={**params, "limit": PAGE, "offset": len(found)}
            )
            found += page[key]
            if not page[key] or len(found) >= page["total"]:
                return found

    async def _upload(self) -> str:
        """Put this server's tarball in the service's store; its tarball_path."""
        created = await self._call(
            "POST",
            "/uploads",
            params={"name": self.upload_name},
            content=self.tarball,
            headers={**self.headers, "Content-Type": "application/gzip"},
        )
        log.info("uploaded automation tarball %s", self.upload_name)
        return created["tarball_path"]

    async def _sync_source(self) -> None:
        """Register the forge as an event source (`automation/webhook_router.py`),
        verified with the secret the forge's own webhook signs with.

        The service never gives a secret back, so the source's name carries a
        digest of it, and a rotated secret is a delete and a recreate."""
        if not self.source or not self.secret_file.exists():
            return
        secret = self.secret_file.read_text().strip()
        if not secret:
            # The forge has not been given its webhook yet.
            return
        name = f"{self.source} ({hashlib.sha256(secret.encode()).hexdigest()[:12]})"
        have = next(
            (
                w
                for w in await self._all("/webhooks", "webhooks")
                if w["source"] == self.source
            ),
            None,
        )
        if have and have["name"] == name:
            return
        if have:
            await self._call("DELETE", f"/webhooks/{have['id']}")
        await self._call(
            "POST",
            "/webhooks",
            json={
                "name": name,
                "source": self.source,
                # Forgejo's `gitea` payload format: a hex HMAC-SHA256 of the
                # body in X-Gitea-Signature, and the event's action in `action`.
                "signature_header": "X-Gitea-Signature",
                "signature_scheme": "hmac_sha256_hex",
                "event_key_expr": "action",
                "webhook_secret": secret,
            },
        )
        log.info("event source %s registered", self.source)

    async def sync(self) -> None:
        """Make the automation service's copy of our automations match the
        definitions: create the missing, update the changed, delete the rest."""
        await self._sync_source()
        wanted = self.definitions()
        uploads = {
            u["tarball_path"]: u
            for u in await self._all("/uploads", "uploads", status="COMPLETED")
            if u["name"].startswith(UPLOAD_PREFIX)
        }
        current = (
            next((p for p, u in uploads.items() if u["name"] == self.upload_name), None)
            or await self._upload()
        )
        seen: set[str] = set()
        for have in await self._all("", "automations"):
            if have["tarball_path"] != current and have["tarball_path"] not in uploads:
                continue
            name = have["name"]
            if name not in wanted or name in seen:
                await self._call("DELETE", f"/{have['id']}")
                log.info("automation %s deleted", name)
                continue
            seen.add(name)
            want = wanted[name]
            patch: dict[str, Any] = {}
            if have["tarball_path"] != current:
                patch["tarball_path"] = current
            if not _covers(have["trigger"], want["trigger"]):
                patch["trigger"] = want["trigger"]
            if want.get("timeout") and have["timeout"] != want["timeout"]:
                patch["timeout"] = want["timeout"]
            # Only ever switched off from here: the service disables an
            # automation that keeps failing, and that should stick.
            if want.get("enabled") is False and have["enabled"]:
                patch["enabled"] = False
            if patch:
                await self._call("PATCH", f"/{have['id']}", json=patch)
                log.info("automation %s updated: %s", name, ", ".join(patch))
        for name in wanted.keys() - seen:
            want = wanted[name]
            body = {
                "name": name,
                "trigger": want["trigger"],
                "tarball_path": current,
                "entrypoint": ENTRYPOINT,
            }
            if want.get("timeout"):
                body["timeout"] = want["timeout"]
            created = await self._call("POST", json=body)
            if want.get("enabled") is False:
                await self._call("PATCH", f"/{created['id']}", json={"enabled": False})
            log.info("automation %s created", name)
        for path, upload in uploads.items():
            if path != current:
                await self._call("DELETE", f"/uploads/{upload['id']}")
