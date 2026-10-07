"""Declarative automations, run by the automation service in cloud mode.

The automation service owns scheduling, webhook matching and run bookkeeping,
and gives every run a sandbox of its own (`automation/backends/cloud.py`). Its
own run scripts build an agent from an LLM API key, which this deployment
does not have, so each automation here is a *custom* one
(`automation/schemas.py`, CreateAutomationRequest): the same small tarball,
whose entry point asks this server to start the conversation.

Definitions come from a file (values.yaml `automations`) and are pushed to the
automation service. An automation is ours if it runs our tarball; anything
else in that service is left alone.
"""

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
        # Fetched from inside the sandbox, which reaches this server on the
        # webhook port only.
        self.tarball_url = f"{settings.webhook_url}/automation/run.tar.gz"
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
        resp = await self.http.request(
            method, f"{self.api}{path}", headers=self.headers, timeout=30, **kw
        )
        resp.raise_for_status()
        return resp.json() if resp.content else None

    async def _ours(self) -> list[dict[str, Any]]:
        found: list[dict[str, Any]] = []
        offset = 0
        while True:
            page = await self._call("GET", params={"limit": PAGE, "offset": offset})
            found += page["automations"]
            offset += PAGE
            if offset >= page["total"]:
                break
        return [a for a in found if a["tarball_path"] == self.tarball_url]

    async def sync(self) -> None:
        """Make the automation service's copy of our automations match the
        definitions: create the missing, update the changed, delete the rest."""
        wanted = self.definitions()
        seen: set[str] = set()
        for have in await self._ours():
            name = have["name"]
            if name not in wanted or name in seen:
                await self._call("DELETE", f"/{have['id']}")
                log.info("automation %s deleted", name)
                continue
            seen.add(name)
            want = wanted[name]
            patch: dict[str, Any] = {}
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
                "tarball_path": self.tarball_url,
                "entrypoint": ENTRYPOINT,
            }
            if want.get("timeout"):
                body["timeout"] = want["timeout"]
            created = await self._call("POST", json=body)
            if want.get("enabled") is False:
                await self._call("PATCH", f"/{created['id']}", json={"enabled": False})
            log.info("automation %s created", name)
