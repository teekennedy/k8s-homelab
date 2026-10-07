"""App conversations: one per sandbox, started through a polled start task,
with history kept here so it outlives the sandbox.

Shapes are the frontend's `AppConversation` / `AppConversationStartTask`
(`api/conversation-service/agent-server-conversation-service.types.d.ts`).
The frontend polls a start task to READY, then fetches the conversation and
talks to its `conversation_url` — a `/runtime/<sandbox>` URL — directly.
"""

import asyncio
import json
import logging
import time
import uuid
from datetime import UTC, datetime
from typing import Any

import httpx

from .db import Database, now
from .sandboxes import SandboxManager, Status
from .secrets_store import SecretsStore
from .settings_store import SettingsStore

log = logging.getLogger(__name__)

WORKING_DIR = "/workspace/project"
TERMINAL = ("READY", "ERROR")
CONVERSATION_SORTS = {
    "CREATED_AT": "created_at ASC",
    "CREATED_AT_DESC": "created_at DESC",
    "UPDATED_AT": "updated_at ASC",
    "UPDATED_AT_DESC": "updated_at DESC",
}
EVENT_SORTS = {"TIMESTAMP": "ASC", "TIMESTAMP_DESC": "DESC"}
# Display title until the user renames it; the agent server's own titling
# uses the agent's LLM, which for an ACP agent is not a litellm model.
TITLE_LENGTH = 60


def conversation_id(value: str) -> str:
    """Canonical (dashed) form; webhooks use the hex form."""
    return str(uuid.UUID(value))


def _naive_utc(ts: str) -> str:
    """Event timestamps are the agent server's naive UTC isoformat; compare
    filter values in the same form."""
    parsed = datetime.fromisoformat(ts.replace("Z", "+00:00"))
    if parsed.tzinfo:
        parsed = parsed.astimezone(UTC).replace(tzinfo=None)
    return parsed.isoformat()


def _title_from(request: dict[str, Any]) -> str | None:
    if request.get("title"):
        return request["title"]
    message = request.get("initial_message") or {}
    for part in message.get("content") or []:
        text = (part.get("text") or "").strip()
        if text:
            first_line = text.splitlines()[0]
            if len(first_line) > TITLE_LENGTH:
                return first_line[: TITLE_LENGTH - 1].rstrip() + "…"
            return first_line
    return None


class ConversationService:
    def __init__(
        self,
        db: Database,
        sandboxes: SandboxManager,
        settings: SettingsStore,
        secrets: SecretsStore,
        http: httpx.AsyncClient,
        public_url: str,
        default_spec: str,
        start_timeout: float,
    ):
        self.db = db
        self.sandboxes = sandboxes
        self.settings = settings
        self.secrets = secrets
        self.http = http
        self.public_url = public_url
        self.default_spec = default_spec
        self.start_timeout = start_timeout
        self._running: set[asyncio.Task] = set()

    # --- sandbox spec -------------------------------------------------------

    def spec_for(self, profile_id: str | None) -> str:
        """The sandbox spec a conversation on this agent profile runs in: the
        spec the profile is named after — `isolated`, or `isolated-<anything>`
        — and the default spec for every other profile."""
        name = self.settings.profile_name(profile_id) or ""
        matches = [
            spec
            for spec in self.sandboxes.specs
            if name == spec or name.startswith(f"{spec}-")
        ]
        return max(matches, key=len, default=self.default_spec)

    # --- start tasks --------------------------------------------------------

    def _set_task(self, task_id: str, **fields: Any) -> None:
        sets = ", ".join(f"{k} = ?" for k in fields)
        self.db.run(
            f"UPDATE start_tasks SET {sets}, updated_at = ? WHERE id = ?",
            *fields.values(),
            now(),
            task_id,
        )

    def task_view(self, row: dict[str, Any]) -> dict[str, Any]:
        sandbox_id = row["sandbox_id"]
        return {
            "id": row["id"],
            "created_by_user_id": row["created_by"],
            "status": row["status"],
            "detail": row["detail"],
            "app_conversation_id": row["app_conversation_id"],
            "agent_server_url": (
                f"{self.public_url}/runtime/{sandbox_id}"
                if row["status"] == "READY"
                else None
            ),
            "sandbox_id": sandbox_id,
            "request": json.loads(row["request"]),
            "created_at": row["created_at"],
            "updated_at": row["updated_at"],
        }

    def tasks(self, ids: list[str]) -> list[dict[str, Any] | None]:
        rows = [self.db.one("SELECT * FROM start_tasks WHERE id = ?", i) for i in ids]
        return [self.task_view(r) if r else None for r in rows]

    def start(self, request: dict[str, Any], created_by: str) -> dict[str, Any]:
        task_id = uuid.uuid4().hex
        ts = now()
        self.db.run(
            "INSERT INTO start_tasks (id, status, request, created_by, created_at,"
            " updated_at) VALUES (?, 'WORKING', ?, ?, ?, ?)",
            task_id,
            json.dumps(request),
            created_by,
            ts,
            ts,
        )
        job = asyncio.create_task(self._run(task_id, request, created_by))
        self._running.add(job)
        job.add_done_callback(self._running.discard)
        return self.tasks([task_id])[0]

    async def abandon_unfinished_tasks(self) -> None:
        """A start task runs in this process; one left mid-flight by a restart
        will never finish, so say so rather than letting the UI poll forever,
        and delete the sandbox it had created for a conversation that never
        started."""
        stranded = self.db.all(
            "SELECT sandbox_id FROM start_tasks"
            " WHERE status NOT IN ('READY', 'ERROR') AND sandbox_id IS NOT NULL"
        )
        for row in stranded:
            await self.sandboxes.delete(row["sandbox_id"])
        self.db.run(
            "UPDATE start_tasks SET status = 'ERROR',"
            " detail = 'The app server restarted while this conversation was"
            " starting. Start it again.', updated_at = ?"
            " WHERE status NOT IN ('READY', 'ERROR')",
            now(),
        )

    async def _wait_running(self, sandbox_id: str) -> None:
        deadline = time.monotonic() + self.start_timeout
        while time.monotonic() < deadline:
            status = (await self.sandboxes.statuses()).get(sandbox_id, "MISSING")
            if status == "RUNNING":
                return
            if status in ("ERROR", "MISSING"):
                raise RuntimeError(f"sandbox {sandbox_id} is {status}")
            await asyncio.sleep(2)
        raise TimeoutError(
            f"sandbox {sandbox_id} was not running after {self.start_timeout:.0f}s"
        )

    async def _run(
        self, task_id: str, request: dict[str, Any], created_by: str
    ) -> None:
        sandbox_id = None
        try:
            profile_id = request.get("agent_profile_id")
            agent_settings = self.settings.resolve_agent(profile_id)
            self._set_task(task_id, status="WAITING_FOR_SANDBOX")
            sandbox = await self.sandboxes.create(self.spec_for(profile_id), created_by)
            sandbox_id = sandbox["id"]
            self._set_task(task_id, sandbox_id=sandbox_id)
            await self._wait_running(sandbox_id)

            self._set_task(task_id, status="STARTING_CONVERSATION")
            info = await self._start_on_sandbox(sandbox, request, agent_settings)
            self._insert_conversation(
                info, sandbox_id, request, agent_settings, created_by
            )
            self._set_task(task_id, status="READY", app_conversation_id=info["id"])
            log.info("conversation %s started on %s", info["id"], sandbox_id)
        except Exception as e:
            log.exception("start task %s failed", task_id)
            self._set_task(task_id, status="ERROR", detail=str(e)[:1000])
            if sandbox_id:
                await self.sandboxes.delete(sandbox_id)

    async def start_in(
        self, sandbox_id: str, request: dict[str, Any], created_by: str
    ) -> str:
        """Start a conversation in a sandbox that already exists and that
        something else owns: an automation run's."""
        sandbox = self.sandboxes.live_row(sandbox_id)
        if sandbox is None:
            raise LookupError(sandbox_id)
        agent_settings = self.settings.resolve_agent(request.get("agent_profile_id"))
        info = await self._start_on_sandbox(sandbox, request, agent_settings)
        self._insert_conversation(info, sandbox_id, request, agent_settings, created_by)
        log.info("conversation %s started on %s", info["id"], sandbox_id)
        return conversation_id(info["id"])

    async def _start_on_sandbox(
        self,
        sandbox: dict[str, Any],
        request: dict[str, Any],
        agent_settings: dict[str, Any],
    ) -> dict[str, Any]:
        """POST /api/conversations on the sandbox's own agent server
        (openhands-sdk `StartConversationRequest`)."""
        body: dict[str, Any] = {
            "workspace": {"working_dir": WORKING_DIR},
            "agent_settings": agent_settings,
            "autotitle": False,
        }
        max_iterations = self.settings.settings()["conversation_settings"].get(
            "max_iterations"
        )
        if max_iterations:
            body["max_iterations"] = max_iterations
        if request.get("conversation_id"):
            body["conversation_id"] = request["conversation_id"]
        if agent_settings.get("agent_kind") == "acp" and agent_settings.get(
            "acp_server"
        ):
            # Where the frontend reads the ACP provider chip from.
            body["tags"] = {"acpserver": agent_settings["acp_server"]}
        secrets = self.secrets.for_conversation()
        if secrets:
            body["secrets"] = secrets
        if request.get("initial_message"):
            body["initial_message"] = {**request["initial_message"], "run": True}
        resp = await self.http.post(
            f"{self.sandboxes.agent_url(sandbox['id'])}/api/conversations",
            json=body,
            headers={"X-Session-API-Key": sandbox["session_api_key"]},
            timeout=120,
        )
        if resp.status_code >= 300:
            raise RuntimeError(
                f"agent server refused the conversation ({resp.status_code}):"
                f" {resp.text[:500]}"
            )
        return resp.json()

    def _insert_conversation(
        self,
        info: dict[str, Any],
        sandbox_id: str,
        request: dict[str, Any],
        agent_settings: dict[str, Any],
        created_by: str,
    ) -> None:
        acp = agent_settings.get("agent_kind") == "acp"
        meta = {
            "selected_repository": request.get("selected_repository"),
            "selected_branch": request.get("selected_branch"),
            "git_provider": request.get("git_provider"),
            "trigger": request.get("trigger"),
            "agent_kind": "acp" if acp else "openhands",
            "acp_server": agent_settings.get("acp_server") if acp else None,
            "llm_model": (
                agent_settings.get("acp_model")
                if acp
                else (agent_settings.get("llm") or {}).get("model")
            ),
            "tags": info.get("tags") or {},
            "metrics": info.get("metrics"),
            "execution_status": info.get("execution_status"),
            "public": False,
        }
        ts = now()
        self.db.run(
            "INSERT INTO conversations (id, sandbox_id, title, created_by, created_at,"
            " updated_at, meta) VALUES (?, ?, ?, ?, ?, ?, ?)",
            conversation_id(info["id"]),
            sandbox_id,
            _title_from(request),
            created_by,
            ts,
            ts,
            json.dumps(meta),
        )

    # --- conversations ------------------------------------------------------

    def row(self, conv_id: str) -> dict[str, Any] | None:
        return self.db.one(
            "SELECT * FROM conversations WHERE id = ? AND deleted_at IS NULL", conv_id
        )

    def view(self, row: dict[str, Any], statuses: dict[str, Status]) -> dict[str, Any]:
        meta = json.loads(row["meta"])
        sandbox_id = row["sandbox_id"]
        sandbox = self.sandboxes.row(sandbox_id)
        status: Status = (
            "MISSING"
            if not sandbox or sandbox["deleted_at"]
            else statuses.get(sandbox_id, "MISSING")
        )
        reachable = status in ("RUNNING", "STARTING", "PAUSED")
        return {
            "id": row["id"],
            "created_by_user_id": row["created_by"],
            "selected_repository": meta.get("selected_repository"),
            "selected_branch": meta.get("selected_branch"),
            "git_provider": meta.get("git_provider"),
            "title": row["title"],
            "trigger": meta.get("trigger"),
            "pr_number": [],
            "agent_kind": meta.get("agent_kind"),
            "launched_agent_profile": None,
            "acp_server": meta.get("acp_server"),
            "tags": meta.get("tags"),
            "llm_model": meta.get("llm_model"),
            "metrics": meta.get("metrics"),
            "created_at": row["created_at"],
            "updated_at": row["updated_at"],
            "execution_status": meta.get("execution_status"),
            "sandbox_status": status,
            # Archived (MISSING/ERROR) conversations render from history alone.
            "conversation_url": (
                f"{self.public_url}/runtime/{sandbox_id}/api/conversations/{row['id']}"
                if reachable
                else None
            ),
            "session_api_key": sandbox["session_api_key"] if reachable else None,
            "sandbox_id": sandbox_id,
            "workspace": {"working_dir": WORKING_DIR},
            "public": bool(meta.get("public")),
            "sub_conversation_ids": [],
        }

    async def views(self, rows: list[dict[str, Any] | None]) -> list[dict | None]:
        statuses = await self.sandboxes.statuses()
        return [self.view(r, statuses) if r else None for r in rows]

    def page(
        self, limit: int, page_id: str | None, sort_order: str
    ) -> tuple[list[dict[str, Any]], str | None]:
        order = CONVERSATION_SORTS.get(
            sort_order, CONVERSATION_SORTS["UPDATED_AT_DESC"]
        )
        offset = int(page_id or 0)
        rows = self.db.all(
            f"SELECT * FROM conversations WHERE deleted_at IS NULL ORDER BY {order}"
            " LIMIT ? OFFSET ?",
            limit + 1,
            offset,
        )
        return rows[:limit], (str(offset + limit) if len(rows) > limit else None)

    def count(self) -> int:
        row = self.db.one(
            "SELECT COUNT(*) AS n FROM conversations WHERE deleted_at IS NULL"
        )
        return row["n"]

    def update(self, conv_id: str, body: dict[str, Any]) -> bool:
        row = self.row(conv_id)
        if row is None:
            return False
        meta = json.loads(row["meta"])
        title = body.get("title", row["title"])
        if "public" in body:
            meta["public"] = bool(body["public"])
        self.db.run(
            "UPDATE conversations SET title = ?, meta = ?, updated_at = ? WHERE id = ?",
            title,
            json.dumps(meta),
            now(),
            conv_id,
        )
        return True

    async def delete(self, conv_id: str) -> bool:
        row = self.row(conv_id)
        if row is None:
            return False
        await self.sandboxes.delete(row["sandbox_id"])
        self.db.run(
            "UPDATE conversations SET deleted_at = ? WHERE id = ?", now(), conv_id
        )
        self.db.run("DELETE FROM events WHERE conversation_id = ?", conv_id)
        return True

    # --- the live runtime ---------------------------------------------------

    async def runtime(self, conv_id: str) -> tuple[str, str] | None:
        """(agent server URL, session key) for a conversation whose sandbox is
        RUNNING, else None: archived and paused conversations have no runtime
        to ask, and the frontend renders them from history."""
        row = self.row(conv_id)
        if row is None:
            return None
        sandbox = self.sandboxes.live_row(row["sandbox_id"])
        if sandbox is None:
            return None
        status = (await self.sandboxes.statuses()).get(sandbox["id"], "MISSING")
        if status != "RUNNING":
            return None
        return self.sandboxes.agent_url(sandbox["id"]), sandbox["session_api_key"]

    def set_llm_model(self, conv_id: str, model: str) -> None:
        row = self.row(conv_id)
        if row is None:
            return
        meta = json.loads(row["meta"])
        meta["llm_model"] = model
        self.db.run(
            "UPDATE conversations SET meta = ?, updated_at = ? WHERE id = ?",
            json.dumps(meta),
            now(),
            conv_id,
        )

    # --- events (webhook sink) ----------------------------------------------

    def _owned(self, sandbox_id: str, conv_id: str) -> bool:
        """Whether a sandbox may write this conversation's history. One that is
        not recorded yet is allowed: the agent server's first webhooks can
        land before the start task records the conversation."""
        row = self.db.one("SELECT sandbox_id FROM conversations WHERE id = ?", conv_id)
        return row is None or row["sandbox_id"] == sandbox_id

    async def events(
        self, sandbox_id: str, conv_hex: str, events: list[dict[str, Any]]
    ) -> None:
        conv_id = conversation_id(conv_hex)
        if not self._owned(sandbox_id, conv_id):
            log.warning("sandbox %s posted events for %s; ignored", sandbox_id, conv_id)
            return
        with self.db.tx() as c:
            c.executemany(
                "INSERT OR IGNORE INTO events (conversation_id, id, timestamp, kind,"
                " body) VALUES (?, ?, ?, ?, ?)",
                [
                    (conv_id, e["id"], e["timestamp"], e.get("kind", ""), json.dumps(e))
                    for e in events
                    if e.get("id") and e.get("timestamp")
                ],
            )

    async def drain(self, sandbox_id: str, conv_id: str) -> None:
        """Read a conversation's events and status straight from its sandbox.
        Webhooks arrive in delayed batches, so a sandbox deleted the moment
        its conversation stops would take the end of the transcript with it."""
        sandbox = self.sandboxes.live_row(sandbox_id)
        if sandbox is None:
            return
        base = f"{self.sandboxes.agent_url(sandbox_id)}/api/conversations/{conv_id}"
        headers = {"X-Session-API-Key": sandbox["session_api_key"]}
        params: dict[str, Any] = {"limit": 100, "sort_order": "TIMESTAMP"}
        while True:
            resp = await self.http.get(
                f"{base}/events/search", params=params, headers=headers, timeout=30
            )
            resp.raise_for_status()
            page = resp.json()
            await self.events(sandbox_id, conv_id, page["items"])
            if not page.get("next_page_id"):
                break
            params["page_id"] = page["next_page_id"]
        resp = await self.http.get(base, headers=headers, timeout=30)
        resp.raise_for_status()
        await self.conversation(sandbox_id, resp.json())

    async def conversation(self, sandbox_id: str, info: dict[str, Any]) -> None:
        conv_id = conversation_id(info["id"])
        row = self.row(conv_id)
        if row is None or row["sandbox_id"] != sandbox_id:
            return
        meta = json.loads(row["meta"])
        for key in ("execution_status", "metrics", "tags"):
            if key in info:
                meta[key] = info[key]
        self.db.run(
            "UPDATE conversations SET meta = ?, updated_at = ? WHERE id = ?",
            json.dumps(meta),
            now(),
            conv_id,
        )

    # --- history ------------------------------------------------------------

    def search_events(
        self,
        conv_id: str,
        limit: int,
        page_id: str | None,
        sort_order: str,
        timestamp_gte: str | None,
        timestamp_lt: str | None,
        kind: str | None,
    ) -> dict[str, Any]:
        """The agent server's own `/events/search` page shape, from the copy
        kept here."""
        where, args = ["conversation_id = ?"], [conv_id]
        if timestamp_gte:
            where.append("timestamp >= ?")
            args.append(_naive_utc(timestamp_gte))
        if timestamp_lt:
            where.append("timestamp < ?")
            args.append(_naive_utc(timestamp_lt))
        if kind:
            where.append("kind = ?")
            args.append(kind)
        direction = EVENT_SORTS.get(sort_order, "ASC")
        offset = int(page_id or 0)
        rows = self.db.all(
            f"SELECT body FROM events WHERE {' AND '.join(where)}"
            f" ORDER BY timestamp {direction}, id {direction} LIMIT ? OFFSET ?",
            *args,
            limit + 1,
            offset,
        )
        return {
            "items": [json.loads(r["body"]) for r in rows[:limit]],
            "next_page_id": str(offset + limit) if len(rows) > limit else None,
        }

    def count_events(self, conv_id: str) -> int:
        return self.db.one(
            "SELECT COUNT(*) AS n FROM events WHERE conversation_id = ?", conv_id
        )["n"]
