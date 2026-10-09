import json
import time
import uuid
from datetime import UTC, datetime, timedelta

import anyio
import httpx
import pytest

from app_server import automation_run, usage_limit

from .test_conversations import CONV_ID, MESSAGE, _post_events, _start

RUN = f"/api/conversations/{CONV_ID}/run"


def _limit_error(reset: datetime, event_id: str = "err1") -> dict:
    body = {
        "error": {
            "type": "usage_limit_reached",
            "message": "The usage limit has been reached",
            "resets_at": int(reset.timestamp()),
            "limit_window_minutes": 300,
        }
    }
    return {
        "id": event_id,
        "timestamp": datetime.now(UTC).replace(tzinfo=None).isoformat(),
        "source": "environment",
        "kind": "ConversationErrorEvent",
        "code": "LLMRateLimitError",
        "detail": f"litellm.RateLimitError: OpenAIException - {json.dumps(body)}",
    }


def _in(**delta) -> datetime:
    return datetime.now(UTC).replace(microsecond=0) + timedelta(**delta)


class FakeAgentServer:
    """A sandbox's agent server, for a conversation that ended in error."""

    def __init__(self) -> None:
        self.status = "error"
        self.run_status = 200
        self.runs = 0
        self.events: list[dict] = []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        path = request.url.path
        if path.endswith("/events/search"):
            return httpx.Response(200, json={"items": self.events})
        if path == RUN:
            self.runs += 1
            return httpx.Response(self.run_status, json={"success": True})
        if request.method == "GET":
            return httpx.Response(
                200, json={"id": CONV_ID, "execution_status": self.status}
            )
        return httpx.Response(201, json={"id": CONV_ID, "execution_status": "running"})


@pytest.fixture
def agent(state, kube) -> FakeAgentServer:
    kube.auto_ready = True
    fake = FakeAgentServer()
    state.conversations.http = httpx.AsyncClient(transport=httpx.MockTransport(fake))
    state.conversations.usage_limit_wait = 1800
    return fake


def _limit(state) -> dict:
    return json.loads(state.conversations.row(CONV_ID)["meta"])["usage_limit"]


def _reconcile(state) -> None:
    async def once() -> None:
        await state.conversations.refresh()
        await state.conversations.resume_due()

    anyio.run(once)


def _reset_passed(state, ago: timedelta) -> None:
    """Move a waiting conversation's reset into the past."""
    row = state.conversations.row(CONV_ID)
    meta = json.loads(row["meta"])
    meta["usage_limit"]["resets_at"] = (datetime.now(UTC) - ago).isoformat()
    state.db.run(
        "UPDATE conversations SET meta = ? WHERE id = ?", json.dumps(meta), CONV_ID
    )


def _history(api, user) -> list[dict]:
    return api.get(
        f"/api/v1/conversation/{CONV_ID}/events/search", headers=user
    ).json()["items"]


def _wait_for(condition) -> None:
    deadline = time.monotonic() + 5
    while not condition():
        assert time.monotonic() < deadline, "condition never held"
        time.sleep(0.02)


def test_reset_time_is_read_from_the_providers_error():
    reset = _in(hours=3)
    assert usage_limit.reset_time(_limit_error(reset)) == reset

    relative = _limit_error(reset)
    relative["timestamp"] = "2026-10-09T05:15:50.562452"
    relative["detail"] = '{"type":"usage_limit_reached","resets_in_seconds":10727}'
    assert usage_limit.reset_time(relative) == datetime(
        2026, 10, 9, 8, 14, 37, 562452, UTC
    )

    # A plain rate limit is the SDK's to retry, and other events are not errors.
    plain = {**_limit_error(reset), "detail": "RateLimitError: slow down"}
    assert usage_limit.reset_time(plain) is None
    assert usage_limit.reset_time({"kind": "MessageEvent", "detail": "x"}) is None


def test_a_near_reset_is_waited_out_and_the_conversation_continues(
    api, hooks, user, state, kube, agent
):
    sandbox_id = _start(api, user)["sandbox_id"]
    error = _limit_error(_in(minutes=10))
    _post_events(hooks, api, user, sandbox_id, [error])

    assert _limit(state)["state"] == "waiting"
    assert kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Running"
    # Not idle, whatever the collector's clock says.
    assert sandbox_id in state.conversations.sandbox_use()[0]
    note = _history(api, user)[-1]
    assert note["kind"] == "MessageEvent" and note["parent_id"] == "err1"
    text = note["llm_message"]["content"][0]["text"]
    assert "Usage limit reached" in text and "continue by itself" in text

    # Redelivered: no second note.
    _post_events(hooks, api, user, sandbox_id, [error])
    assert len(_history(api, user)) == 2

    _reconcile(state)
    assert agent.runs == 0, "the limit has not reset yet"

    _reset_passed(state, timedelta(minutes=1))
    _reconcile(state)
    assert agent.runs == 1 and _limit(state)["state"] == "resumed"
    _reconcile(state)
    assert agent.runs == 1


def test_a_failed_continue_is_retried_until_it_is_too_late(
    api, hooks, user, state, agent
):
    sandbox_id = _start(api, user)["sandbox_id"]
    _post_events(hooks, api, user, sandbox_id, [_limit_error(_in(minutes=10))])
    agent.run_status = 503

    _reset_passed(state, timedelta(minutes=1))
    _reconcile(state)
    _reconcile(state)
    assert agent.runs == 2 and _limit(state)["state"] == "waiting"

    _reset_passed(state, timedelta(minutes=30))
    _reconcile(state)
    assert agent.runs == 2 and _limit(state)["state"] == "expired"


def test_a_conversation_continued_by_hand_is_left_alone(api, hooks, user, state, agent):
    sandbox_id = _start(api, user)["sandbox_id"]
    _post_events(hooks, api, user, sandbox_id, [_limit_error(_in(minutes=10))])
    agent.status = "running"

    _reset_passed(state, timedelta(minutes=1))
    _reconcile(state)
    assert agent.runs == 0 and _limit(state)["state"] == "superseded"


def test_a_distant_reset_suspends_the_sandbox(api, hooks, user, state, kube, agent):
    sandbox_id = _start(api, user)["sandbox_id"]
    error = _limit_error(_in(hours=3))
    # Still in the agent server's webhook buffer when the pod goes.
    agent.events = [error, {**error, "id": "late", "kind": "ObservationEvent"}]
    _post_events(hooks, api, user, sandbox_id, [error])

    _wait_for(lambda: kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Suspended")
    assert _limit(state)["state"] == "suspended"
    history = _history(api, user)
    assert "late" in [e["id"] for e in history]
    notes = [e for e in history if e["kind"] == "MessageEvent"]
    assert len(notes) == 1
    assert "suspended" in notes[0]["llm_message"]["content"][0]["text"]

    _reset_passed(state, timedelta(minutes=1))
    _reconcile(state)
    assert agent.runs == 0


def test_an_automation_runs_sandbox_is_not_ours_to_hold(
    api, hooks, state, kube, agent, service_auth
):
    sandbox = api.post("/api/v1/sandboxes", headers=service_auth).json()
    key = {"X-Session-API-Key": sandbox["session_api_key"]}
    (state.settings.automations_file).write_text(json.dumps({"a": {"prompt": "go"}}))
    r = hooks.post(
        f"/sandboxes/{sandbox['id']}/automation/conversations",
        json={"automation": "a"},
        headers=key,
    )
    assert r.status_code == 200, r.text
    hooks.post(
        f"/sandboxes/{sandbox['id']}/events/{uuid.UUID(CONV_ID).hex}",
        json=[_limit_error(_in(minutes=10))],
        headers=key,
    )
    assert _limit(state)["state"] == "ended"
    assert kube.sandbox(sandbox["id"])["spec"]["operatingMode"] == "Running"


def test_a_conversation_started_without_a_message_is_titled_by_its_first(
    api, hooks, user, agent
):
    sandbox_id = _start(api, user, initial_message=None)["sandbox_id"]
    conv = api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert conv["title"] is None

    def message(i: int, text: str) -> dict:
        return {
            "id": f"m{i}",
            "timestamp": f"2026-10-01T06:34:{i:02d}.000000",
            "kind": "MessageEvent",
            "source": "user",
            "llm_message": {
                "role": "user",
                "content": [{"type": "text", "text": text}],
            },
        }

    _post_events(hooks, api, user, sandbox_id, [message(1, "Analyze this log\nplease")])
    _post_events(hooks, api, user, sandbox_id, [message(2, "And another thing")])
    conv = api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert conv["title"] == "Analyze this log"


def test_backfill_titles_and_explains_what_an_earlier_version_recorded(
    api, user, state, kube, agent
):
    sandbox_id = _start(api, user, initial_message=None)["sandbox_id"]
    error = _limit_error(_in(hours=-3))
    first = {
        "id": "m1",
        "timestamp": "2026-10-01T06:34:01.000000",
        "kind": "MessageEvent",
        "source": "user",
        "llm_message": MESSAGE,
    }
    for event in (first, error):
        state.db.run(
            "INSERT INTO events (conversation_id, id, timestamp, kind, body)"
            " VALUES (?, ?, ?, ?, ?)",
            CONV_ID,
            event["id"],
            event["timestamp"],
            event["kind"],
            json.dumps(event),
        )

    state.conversations.backfill()
    state.conversations.backfill()
    assert state.conversations.row(CONV_ID)["title"] == "Reply with PONG."
    assert _limit(state)["state"] == "ended"
    assert kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Running"
    notes = [e for e in _history(api, user) if e.get("parent_id") == "err1"]
    assert len(notes) == 1
    assert "It reset at" in notes[0]["llm_message"]["content"][0]["text"]


def test_a_failed_run_says_why_the_conversation_ended(monkeypatch):
    error = _limit_error(_in(hours=3))

    def call(method: str, url: str, key: str, body: dict | None = None) -> dict:
        if "/events/search" in url:
            return {"items": [{"kind": "ObservationEvent"}, error]}
        return {"execution_status": "error"}

    monkeypatch.setattr(automation_run, "call", call)
    outcome = automation_run.wait("http://agent", CONV_ID, "key")
    assert outcome.startswith("conversation ended error: LLMRateLimitError: ")
    assert "usage_limit_reached" in outcome
