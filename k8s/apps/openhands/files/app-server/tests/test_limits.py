import dataclasses
import json
from datetime import UTC, datetime, timedelta

import anyio

from app_server.sandboxes import Limits

from .test_conversations import CONV_ID, _start, agent  # noqa: F401


def _ago(**delta) -> str:
    return (datetime.now(UTC) - timedelta(**delta)).isoformat()


def _limit(state, **limits) -> None:
    state.sandboxes.limits = dataclasses.replace(Limits(), **limits)


def _collect(state) -> None:
    anyio.run(state.sandboxes.collect, *state.conversations.sandbox_use())


def _create(api, auth) -> str:
    r = api.post("/api/v1/sandboxes", headers=auth)
    assert r.status_code == 200, r.text
    return r.json()["id"]


def test_running_sandboxes_are_capped_and_suspended_ones_do_not_count(
    api, user, state, service_auth
):
    _limit(state, max_running=2)
    first = _create(api, user)
    _create(api, user)
    refused = api.post("/api/v1/sandboxes", headers=service_auth)
    # The automation service marks a run skipped on a 429 with a message.
    assert refused.status_code == 429 and "limit" in refused.json()["message"]
    api.post(f"/api/v1/sandboxes/{first}/pause", headers=user)
    _create(api, service_auth)


def test_a_conversation_at_capacity_fails_with_the_reason(api, user, state, agent):
    _limit(state, max_running=1)
    _create(api, user)
    task = _start(api, user)
    assert task["status"] == "ERROR" and "limit" in task["detail"]


def test_an_idle_sandbox_is_suspended_then_deleted(api, user, state, kube, agent):
    _limit(state, idle_suspend=3600, suspended_delete=86400)
    sandbox_id = _start(api, user)["sandbox_id"]
    _collect(state)
    assert kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Running"

    state.db.run("UPDATE sandboxes SET last_active_at = ?", _ago(hours=2))
    # Idle by the clock, but its agent is still working.
    _collect(state)
    assert kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Running"

    meta = json.loads(state.conversations.row(CONV_ID)["meta"])
    meta["execution_status"] = "finished"
    state.db.run("UPDATE conversations SET meta = ?", json.dumps(meta))
    _collect(state)
    assert kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Suspended"

    _collect(state)
    assert kube.sandbox(sandbox_id) is not None
    state.db.run("UPDATE sandboxes SET suspended_at = ?", _ago(days=2))
    _collect(state)
    assert kube.sandbox(sandbox_id) is None
    # The conversation outlives it, as history.
    conv = api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert conv["sandbox_status"] == "MISSING"


def test_resuming_clears_the_suspension(api, user, state, kube):
    _limit(state, suspended_delete=86400)
    sandbox_id = _create(api, user)
    api.post(f"/api/v1/sandboxes/{sandbox_id}/pause", headers=user)
    state.db.run("UPDATE sandboxes SET suspended_at = ?", _ago(days=2))
    api.post(f"/api/v1/sandboxes/{sandbox_id}/resume", headers=user)
    _collect(state)
    assert kube.sandbox(sandbox_id) is not None


def test_a_service_sandbox_with_no_conversation_is_reaped(
    api, user, state, kube, service_auth
):
    _limit(state, service_orphan=600, service_max=7200, idle_suspend=60)
    orphan = _create(api, service_auth)
    browser = _create(api, user)
    _collect(state)
    assert kube.sandbox(orphan) is not None

    state.db.run(
        "UPDATE sandboxes SET created_at = ? WHERE id = ?", _ago(hours=1), orphan
    )
    _collect(state)
    assert kube.sandbox(orphan) is None
    # A browser's sandbox is never an orphan, only idle.
    assert kube.sandbox(browser) is not None


def test_a_service_sandbox_with_a_conversation_lives_until_the_cap(
    api, state, kube, service_auth, agent
):
    _limit(state, service_orphan=600, service_max=7200, idle_suspend=60)
    sandbox_id = _create(api, service_auth)
    kube.set_ready(sandbox_id)
    anyio.run(state.conversations.start_in, sandbox_id, {"title": "run"}, "automation")
    state.db.run(
        "UPDATE sandboxes SET created_at = ?, last_active_at = ?",
        _ago(hours=1),
        _ago(hours=1),
    )
    _collect(state)
    # Neither orphaned nor suspended for idleness: the run owns its lifetime.
    assert kube.sandbox(sandbox_id)["spec"]["operatingMode"] == "Running"
    state.db.run("UPDATE sandboxes SET created_at = ?", _ago(hours=3))
    _collect(state)
    assert kube.sandbox(sandbox_id) is None


def test_events_are_capped_per_conversation_oldest_first(
    api, user, state, hooks, agent
):
    state.conversations.max_events = 3
    sandbox_id = _start(api, user)["sandbox_id"]
    key = state.sandboxes.row(sandbox_id)["session_api_key"]
    events = [
        {"id": f"e{i}", "timestamp": f"2026-01-01T00:00:0{i}", "kind": "MessageEvent"}
        for i in range(5)
    ]
    r = hooks.post(
        f"/sandboxes/{sandbox_id}/events/{CONV_ID.replace('-', '')}",
        headers={"X-Session-API-Key": key},
        json=events,
    )
    assert r.status_code == 200
    page = state.conversations.search_events(
        CONV_ID, 10, None, "TIMESTAMP", None, None, None
    )
    assert [e["id"] for e in page["items"]] == ["e2", "e3", "e4"]
