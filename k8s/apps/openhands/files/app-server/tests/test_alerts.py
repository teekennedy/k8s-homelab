import dataclasses
import hashlib
import hmac
import json

import httpx
import pytest
from fastapi.testclient import TestClient

from app_server import alerts
from app_server.main import build_alerts
from tests.test_automations import FakeAutomationService, _define, service_client
from tests.test_conversations import CONV_ID, FakeAgentServer

TOKEN = {"Authorization": "Bearer am-token"}
TRIAGE = {
    "trigger": {
        "type": "event",
        "source": "alertmanager",
        "on": "firing",
        "destination": "continue_conversation",
        "subject_key_expr": "'alerts'",
        "turn_text_expr": "summary",
    },
    "prompt": "Investigate.\n",
    "repository": "ops/k8s-homelab",
    "branch": "main",
    "model": "haiku",
    "notify": "discord",
}


def _alert(name: str, status: str = "firing", **labels) -> dict:
    return {
        "status": status,
        "labels": {"alertname": name, **labels},
        "annotations": {"summary": f"{name} is happening"},
        "startsAt": "2026-11-16T10:00:00Z",
        "generatorURL": "http://prometheus/graph",
        "fingerprint": hashlib.sha1(name.encode()).hexdigest()[:16],
        "endsAt": "0001-01-01T00:00:00Z",
    }


def _notification(*alerts_: dict) -> dict:
    return {"version": "4", "status": "firing", "alerts": list(alerts_)}


RUN_ID = "0cd7e625-a5f0-4164-a27c-d77ca5999e90"


@pytest.fixture
def settings(settings, tmp_path):
    """conftest's, with alerts served and a Discord webhook to report to."""
    token = tmp_path / "alerts-token"
    token.write_text("am-token\n")
    discord = tmp_path / "discord-url"
    discord.write_text("https://discord.example/api/webhooks/1/x\n")
    return dataclasses.replace(
        settings,
        alerts_port=8082,
        alerts_token_file=token,
        alerts_cooldown_seconds=3600,
        discord_webhook_file=discord,
        discord_avatar_url="https://example/avatar.png",
    )


@pytest.fixture
def service(state) -> FakeAutomationService:
    fake = FakeAutomationService()
    state.automations.http = service_client(fake)
    state.alerts.http = httpx.AsyncClient(transport=httpx.MockTransport(fake.event))
    return fake


@pytest.fixture
def intake(state, service) -> TestClient:
    return TestClient(build_alerts(state))


def _events(service) -> list[dict]:
    return [json.loads(r.content) for r in service.calls if "/events/" in r.url.path]


def test_alertmanager_must_present_the_token(intake, state):
    body = _notification(_alert("KubePodCrashLooping"))
    assert intake.post("/alertmanager", json=body).status_code == 401
    wrong = {"Authorization": "Bearer nope"}
    assert intake.post("/alertmanager", json=body, headers=wrong).status_code == 401
    state.settings.alerts_token_file.write_text("")
    assert intake.post("/alertmanager", json=body, headers=TOKEN).status_code == 503


def test_firing_alerts_are_forwarded_signed(intake, service, state):
    body = _notification(
        _alert("LonghornVolumeDegraded", volume="pvc-1"),
        _alert("LonghornVolumeDegraded", volume="pvc-2"),
        _alert("KubeJobFailed", status="resolved"),
    )
    r = intake.post("/alertmanager", json=body, headers=TOKEN)
    assert r.status_code == 200 and r.json() == {"forwarded": 2}

    (request,) = [c for c in service.calls if "/events/" in c.url.path]
    assert request.url.path.endswith(
        "/events/00000000-0000-4000-8000-000000000001/alertmanager"
    )
    secret = alerts.source_secret("automation-key")
    expected = hmac.new(secret.encode(), request.content, hashlib.sha256).hexdigest()
    assert request.headers[alerts.SIGNATURE_HEADER] == expected

    event = json.loads(request.content)
    assert event["status"] == "firing" and event["alert_count"] == 2
    assert [a["labels"]["volume"] for a in event["alerts"]] == ["pvc-1", "pvc-2"]
    assert "endsAt" not in event["alerts"][0]
    assert event["summary"].startswith("2 alert(s) firing:")
    assert "LonghornVolumeDegraded {volume=pvc-1}" in event["summary"]


def test_an_alert_name_cools_down_once_forwarded(intake, service):
    first = _notification(_alert("LonghornVolumeDegraded", volume="pvc-1"))
    assert intake.post("/alertmanager", json=first, headers=TOKEN).json() == {
        "forwarded": 1
    }
    # Another replica, or more instances joining the group: dropped.
    later = _notification(
        _alert("LonghornVolumeDegraded", volume="pvc-9"), _alert("KubePodNotReady")
    )
    assert intake.post("/alertmanager", json=later, headers=TOKEN).json() == {
        "forwarded": 1
    }
    assert [a["labels"]["alertname"] for a in _events(service)[-1]["alerts"]] == [
        "KubePodNotReady"
    ]
    assert intake.post("/alertmanager", json=later, headers=TOKEN).json() == {
        "forwarded": 0
    }
    assert len(_events(service)) == 2


def test_the_cooldown_starts_when_a_run_completes(intake, service, state, monkeypatch):
    body = _notification(_alert("KubePodNotReady"))

    def forward() -> int:
        return intake.post("/alertmanager", json=body, headers=TOKEN).json()[
            "forwarded"
        ]

    assert forward() == 1
    # Held while a run may still be on it ...
    assert forward() == 0
    # ... but a run that was skipped or failed never reports, and the hold lapses.
    with monkeypatch.context() as m:
        m.setattr(alerts, "PENDING_SECONDS", 0)
        assert forward() == 1
    # Once a run completes, the name cools down for the whole cooldown, and
    # outlives the hold.
    state.alerts.triaged()
    monkeypatch.setattr(alerts, "PENDING_SECONDS", 0)
    assert forward() == 0


def test_a_refused_event_is_retried_not_cooled_down(intake, service):
    service.refuse_events = True
    body = _notification(_alert("KubePodNotReady"))
    assert intake.post("/alertmanager", json=body, headers=TOKEN).status_code == 502
    service.refuse_events = False
    assert intake.post("/alertmanager", json=body, headers=TOKEN).json() == {
        "forwarded": 1
    }


@pytest.mark.anyio
async def test_alertmanager_is_registered_as_an_event_source(state, service):
    await state.automations.sync()
    (source,) = service.sources
    assert source["source"] == "alertmanager"
    assert source["webhook_secret"] == alerts.source_secret("automation-key")
    assert source["signature_header"] == alerts.SIGNATURE_HEADER
    assert source["event_key_expr"] == "status"

    state.settings.forge_webhook_secret_file.write_text("forge-secret")
    await state.automations.sync()
    assert sorted(s["source"] for s in service.sources) == ["alertmanager", "forgejo"]
    assert len(service.sources) == 2


def test_a_run_checks_out_its_repository_on_its_model(api, hooks, state, user):
    _define(state, triage=TRIAGE)
    sandbox = api.post("/api/v1/sandboxes", headers=user).json()
    agent = FakeAgentServer()
    state.conversations.http = httpx.AsyncClient(transport=httpx.MockTransport(agent))
    r = hooks.post(
        f"/sandboxes/{sandbox['id']}/automation/conversations",
        json={"automation": "triage", "event": {"summary": "1 alert(s) firing"}},
        headers={"X-Session-API-Key": sandbox["session_api_key"]},
    )
    assert r.status_code == 200
    (clone,) = agent.commands
    assert "ops/k8s-homelab" in clone and "main" in clone
    assert agent.body()["agent_settings"]["acp_model"] == "haiku"

    conv = api.get("/api/v1/app-conversations", params={"ids": CONV_ID}, headers=user)
    assert conv.json()[0]["selected_repository"] == "ops/k8s-homelab"


def _finish(message: str) -> dict:
    return {
        "id": "e2",
        "timestamp": "2026-01-01T00:00:02",
        "kind": "ActionEvent",
        "source": "agent",
        "tool_name": "finish",
        "action": {"message": message},
    }


@pytest.fixture
def discord_posts(hooks, service) -> list[httpx.Request]:
    """What the app server posts to Discord; everything else goes to the
    automation service."""
    posted: list[httpx.Request] = []

    def outside(request: httpx.Request) -> httpx.Response:
        if request.url.host == "discord.example":
            posted.append(request)
            return httpx.Response(204)
        return service(request)

    hooks.app.state.http = httpx.AsyncClient(transport=httpx.MockTransport(outside))
    return posted


def _complete(hooks, sandbox: dict, **body) -> httpx.Response:
    return hooks.post(
        f"/sandboxes/{sandbox['id']}/automation/runs/{RUN_ID}/complete",
        json={"automation": "triage", **body},
        headers={"X-Session-API-Key": sandbox["session_api_key"]},
    )


def test_a_finished_run_is_reported_to_discord(api, hooks, state, discord_posts, user):
    _define(state, triage=TRIAGE)
    sandbox = api.post("/api/v1/sandboxes", headers=user).json()
    events = [
        {
            "id": "e1",
            "timestamp": "2026-01-01T00:00:01",
            "kind": "MessageEvent",
            "source": "agent",
            "llm_message": {"content": [{"type": "text", "text": "Looking."}]},
        },
        _finish("Cause: disk on borg-2 died. PR: https://git/pulls/9"),
    ]

    def agent(request: httpx.Request) -> httpx.Response:
        if request.url.path.endswith("/events/search"):
            return httpx.Response(200, json={"items": events, "next_page_id": None})
        return FakeAgentServer()(request)

    state.conversations.http = httpx.AsyncClient(transport=httpx.MockTransport(agent))
    hooks.post(
        f"/sandboxes/{sandbox['id']}/automation/conversations",
        json={"automation": "triage"},
        headers={"X-Session-API-Key": sandbox["session_api_key"]},
    )
    r = _complete(hooks, sandbox, status="COMPLETED", conversation_id=CONV_ID)
    # The service's schema forbids fields it does not know.
    assert "automation" not in r.json()["relayed"]
    (message,) = discord_posts
    embed = json.loads(message.content)["embeds"][0]
    assert embed["title"] == "triage"
    assert embed["description"].startswith("Cause: disk on borg-2 died.")
    assert embed["url"] == f"https://openhands.example/conversations/{CONV_ID}"
    posted = json.loads(message.content)
    assert posted["allowed_mentions"] == {"parse": []}
    assert posted["username"] == "OpenHands"
    assert posted["avatar_url"] == "https://example/avatar.png"

    # A run that never got a conversation still says so.
    discord_posts.clear()
    _complete(hooks, sandbox, status="FAILED", error="could not clone")
    (failed,) = discord_posts
    embed = json.loads(failed.content)["embeds"][0]
    assert "could not clone" in embed["description"]
    assert embed["color"] == 0xE74C3C


def test_a_run_without_notify_reports_nothing(api, hooks, state, discord_posts, user):
    _define(state, triage={k: v for k, v in TRIAGE.items() if k != "notify"})
    sandbox = api.post("/api/v1/sandboxes", headers=user).json()
    _complete(hooks, sandbox, status="FAILED", error="x")
    assert discord_posts == []


def test_a_completed_alert_run_starts_the_cooldown(
    api, hooks, state, discord_posts, user
):
    _define(state, triage=TRIAGE)
    sandbox = api.post("/api/v1/sandboxes", headers=user).json()
    calls = []
    state.alerts.triaged = lambda: calls.append(1)
    _complete(hooks, sandbox, status="FAILED", error="x")
    assert calls == []
    _complete(hooks, sandbox, status="COMPLETED")
    assert calls == [1]


def test_a_model_for_a_non_acp_profile_is_ignored_with_a_warning(
    state, caplog, monkeypatch
):
    store = state.settings_store
    codex = {"id": "p1", "agent_kind": "openhands", "llm_profile_ref": "codex"}
    monkeypatch.setattr(
        store, "_profiles", lambda: {"active": "p1", "profiles": {"codex": codex}}
    )
    monkeypatch.setattr(store, "_llm_profiles", lambda: {"active": None})
    monkeypatch.setattr(store, "llm_config", lambda name: {"model": name})
    agent = store.resolve_agent(None, acp_model="haiku")
    assert agent["agent_kind"] == "openhands" and "acp_model" not in agent
    assert "model haiku not applied" in caplog.text
