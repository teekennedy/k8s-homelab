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
from datetime import UTC, datetime, timedelta
from typing import Any

import httpx

from . import usage_limit
from .db import Database, now
from .forge import Forge
from .sandboxes import SandboxManager, Status
from .secrets_store import SecretsStore
from .settings_store import SettingsStore

log = logging.getLogger(__name__)

WORKING_DIR = "/workspace/project"
TERMINAL = ("READY", "ERROR")
# Seconds a new conversation's repository may take to clone.
CLONE_TIMEOUT = 300
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
# A conversation waiting out a usage limit is run again this long after the
# reset, and not at all once it is this late: by then its prompt cache is
# cold, and continuing is the user's call.
RESUME_MARGIN = timedelta(seconds=30)
RESUME_GRACE = timedelta(minutes=10)


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
        forge: Forge,
        http: httpx.AsyncClient,
        public_url: str,
        default_spec: str,
        start_timeout: float,
        max_events: int = 0,
        usage_limit_wait: float = 0,
    ):
        self.db = db
        self.sandboxes = sandboxes
        self.settings = settings
        self.secrets = secrets
        self.forge = forge
        self.http = http
        self.public_url = public_url
        self.default_spec = default_spec
        self.start_timeout = start_timeout
        self.max_events = max_events
        # The longest a usage limit is waited out, in seconds; see
        # `_usage_limit_hit`.
        self.usage_limit_wait = usage_limit_wait
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

            if request.get("selected_repository"):
                self._set_task(task_id, status="PREPARING_REPOSITORY")
                await self._clone(sandbox, request)
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

    async def _clone(self, sandbox: dict[str, Any], request: dict[str, Any]) -> None:
        """Check the chosen repository out as the conversation's working
        directory, with the sandbox's own git credentials."""
        repository = request["selected_repository"]
        resp = await self.http.post(
            f"{self.sandboxes.agent_url(sandbox['id'])}/api/bash/execute_bash_command",
            json={
                "command": self.forge.clone_command(
                    repository, request.get("selected_branch")
                ),
                "cwd": WORKING_DIR,
                "timeout": CLONE_TIMEOUT,
            },
            headers={"X-Session-API-Key": sandbox["session_api_key"]},
            timeout=CLONE_TIMEOUT + 30,
        )
        resp.raise_for_status()
        result = resp.json()
        if result.get("exit_code") != 0:
            output = (result.get("stderr") or result.get("stdout") or "").strip()
            raise RuntimeError(
                f"could not clone {repository} into the sandbox"
                f" (spec {sandbox['spec']}): {output[-500:] or 'no output'}"
            )

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
            "stats": info.get("stats"),
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
            # Never null where there is a forge: the frontend words its git
            # actions for GitHub when a conversation names no provider.
            "git_provider": meta.get("git_provider") or self.forge.provider,
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

    def sandbox_use(self) -> tuple[set[str], set[str]]:
        """(busy, claimed) sandbox ids, for the collector: those whose agent
        is running or whose conversation is still starting, and those that
        have a conversation at all."""
        busy = {
            r["sandbox_id"]
            for r in self.db.all(
                "SELECT sandbox_id FROM start_tasks"
                " WHERE status NOT IN ('READY', 'ERROR') AND sandbox_id IS NOT NULL"
            )
        }
        claimed = set()
        for row in self.db.all(
            "SELECT sandbox_id, meta FROM conversations WHERE deleted_at IS NULL"
        ):
            claimed.add(row["sandbox_id"])
            meta = json.loads(row["meta"])
            waiting = (meta.get("usage_limit") or {}).get("state") == "waiting"
            if meta.get("execution_status") == "running" or waiting:
                busy.add(row["sandbox_id"])
        return busy, claimed

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
            if self.max_events:
                c.execute(
                    "DELETE FROM events WHERE conversation_id = ? AND id IN ("
                    "SELECT id FROM events WHERE conversation_id = ?"
                    " ORDER BY timestamp DESC, id DESC LIMIT -1 OFFSET ?)",
                    (conv_id, conv_id, self.max_events),
                )
        # A working agent is activity, whether or not a browser is watching.
        self.sandboxes.touch(sandbox_id)
        self._title_from_events(conv_id, events)
        for event in events:
            reset = usage_limit.reset_time(event)
            if reset:
                self._usage_limit_hit(sandbox_id, conv_id, event, reset)

    def _title_from_events(self, conv_id: str, events: list[dict[str, Any]]) -> None:
        """Title a conversation that was started without a message — one with
        an attachment is — from the first message it is sent."""
        for event in events:
            if event.get("kind") != "MessageEvent" or event.get("source") != "user":
                continue
            title = _title_from({"initial_message": event.get("llm_message")})
            if title:
                self.db.run(
                    "UPDATE conversations SET title = ? WHERE id = ? AND title IS NULL",
                    title,
                    conv_id,
                )
                return

    # --- usage limits -------------------------------------------------------

    def _usage_limit_hit(
        self,
        sandbox_id: str,
        conv_id: str,
        error: dict[str, Any],
        reset: datetime,
        act: bool = True,
    ) -> None:
        """A turn ended on the subscription's usage limit. A reset that is
        near is waited out, and `resume_due` runs the conversation again; for
        one further off the sandbox is suspended and the user decides. Either
        way the transcript says so. An automation run's sandbox is the
        automation service's: the run fails and takes its sandbox with it.

        `act` is False for a limit read back from history, long past."""
        row = self.row(conv_id)
        if row is None or row["sandbox_id"] != sandbox_id:
            return
        meta = json.loads(row["meta"])
        seen = meta.get("usage_limit")
        # Redelivered, or an older one read back by `drain`.
        if seen and datetime.fromisoformat(seen["resets_at"]) >= reset:
            return
        at = datetime.now(UTC)
        sandbox = self.sandboxes.live_row(sandbox_id)
        if not act or sandbox is None or sandbox["owner_kind"] == "service":
            state = "ended"
        elif (reset - at).total_seconds() <= self.usage_limit_wait:
            state = "waiting"
        else:
            state = "suspended"
        meta["usage_limit"] = {
            "resets_at": reset.isoformat(),
            "event_id": error["id"],
            "state": state,
        }
        notice = usage_limit.notice_event(
            error, usage_limit.notice_text(reset, at, state)
        )
        with self.db.tx() as c:
            c.execute(
                "UPDATE conversations SET meta = ? WHERE id = ?",
                (json.dumps(meta), conv_id),
            )
            c.execute(
                "INSERT OR IGNORE INTO events (conversation_id, id, timestamp, kind,"
                " body) VALUES (?, ?, ?, ?, ?)",
                (
                    conv_id,
                    notice["id"],
                    notice["timestamp"],
                    notice["kind"],
                    json.dumps(notice),
                ),
            )
        log.info(
            "conversation %s hit its usage limit, reset %s: %s", conv_id, reset, state
        )
        if state == "suspended":
            job = asyncio.create_task(self._suspend_for_limit(sandbox_id, conv_id))
            self._running.add(job)
            job.add_done_callback(self._running.discard)

    async def _suspend_for_limit(self, sandbox_id: str, conv_id: str) -> None:
        try:
            # The pod takes any events it has not posted yet with it.
            await self.drain(sandbox_id, conv_id)
        except Exception:
            log.exception("could not read the last events of %s", sandbox_id)
        await self.sandboxes.pause(sandbox_id)

    def _set_limit_state(self, conv_id: str, state: str) -> None:
        row = self.row(conv_id)
        if row is None:
            return
        meta = json.loads(row["meta"])
        meta["usage_limit"]["state"] = state
        self.db.run(
            "UPDATE conversations SET meta = ? WHERE id = ?", json.dumps(meta), conv_id
        )

    async def resume_due(self) -> None:
        """Run the conversations whose usage limit has reset. Call after
        `refresh`, so a conversation the user already continued is seen as
        such."""
        at = datetime.now(UTC)
        for row in self.db.all(
            "SELECT id, meta FROM conversations WHERE deleted_at IS NULL"
        ):
            meta = json.loads(row["meta"])
            limit = meta.get("usage_limit") or {}
            if limit.get("state") != "waiting":
                continue
            due = datetime.fromisoformat(limit["resets_at"]) + RESUME_MARGIN
            if at < due:
                continue
            conv_id = row["id"]
            runtime = await self.runtime(conv_id)
            if meta.get("execution_status") != "error" or runtime is None:
                # Continued, or stopped, by hand.
                self._set_limit_state(conv_id, "superseded")
                continue
            if at > due + RESUME_GRACE:
                log.warning("conversation %s: too late to continue it", conv_id)
                self._set_limit_state(conv_id, "expired")
                continue
            url, key = runtime
            try:
                resp = await self.http.post(
                    f"{url}/api/conversations/{conv_id}/run",
                    headers={"X-Session-API-Key": key},
                    timeout=30,
                )
            except httpx.HTTPError as e:
                log.warning("could not continue %s, will retry: %s", conv_id, e)
                continue
            # 409: it is running already.
            if resp.status_code < 300 or resp.status_code == 409:
                log.info("conversation %s continued after its usage limit", conv_id)
                self._set_limit_state(conv_id, "resumed")
            else:
                log.warning(
                    "could not continue %s, will retry (%s): %s",
                    conv_id,
                    resp.status_code,
                    resp.text[:300],
                )

    def backfill(self) -> None:
        """Bring history recorded by an earlier version up to date: titles for
        conversations that never got one, and a note where a usage limit ended
        the transcript."""
        for row in self.db.all(
            "SELECT c.id, e.body FROM conversations c JOIN events e"
            " ON e.conversation_id = c.id WHERE c.title IS NULL"
            " AND e.kind = 'MessageEvent' ORDER BY e.timestamp, e.id"
        ):
            self._title_from_events(row["id"], [json.loads(row["body"])])
        for row in self.db.all(
            "SELECT c.id, c.sandbox_id, e.body FROM conversations c JOIN events e"
            " ON e.conversation_id = c.id WHERE c.deleted_at IS NULL"
            " AND e.kind = 'ConversationErrorEvent' ORDER BY e.timestamp"
        ):
            event = json.loads(row["body"])
            reset = usage_limit.reset_time(event)
            if reset:
                self._usage_limit_hit(
                    row["sandbox_id"], row["id"], event, reset, act=False
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

    async def refresh(self) -> None:
        """Read status and usage from every running sandbox. The agent
        server's conversation webhook fires when a conversation is created,
        paused or deleted, not when a turn ends."""
        statuses = await self.sandboxes.statuses()
        for row in self.db.all(
            "SELECT id, sandbox_id FROM conversations WHERE deleted_at IS NULL"
        ):
            sandbox_id = row["sandbox_id"]
            sandbox = self.sandboxes.live_row(sandbox_id)
            if sandbox is None or statuses.get(sandbox_id) != "RUNNING":
                continue
            try:
                resp = await self.http.get(
                    f"{self.sandboxes.agent_url(sandbox_id)}"
                    f"/api/conversations/{row['id']}",
                    headers={"X-Session-API-Key": sandbox["session_api_key"]},
                    timeout=10,
                )
                resp.raise_for_status()
                await self.conversation(sandbox_id, resp.json())
            except httpx.HTTPError as e:
                # Typically a pod that is Ready before its server listens.
                log.info("refresh of %s on %s failed: %s", row["id"], sandbox_id, e)

    async def conversation(self, sandbox_id: str, info: dict[str, Any]) -> None:
        conv_id = conversation_id(info["id"])
        row = self.row(conv_id)
        if row is None or row["sandbox_id"] != sandbox_id:
            return
        meta = json.loads(row["meta"])
        changed = {
            key: info[key]
            for key in ("execution_status", "metrics", "stats", "tags")
            if key in info and meta.get(key) != info[key]
        }
        if not changed:
            return
        meta.update(changed)
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
