import io
import json
import tarfile

import httpx
import pytest

from tests.test_conversations import CONV_ID, FakeAgentServer

TARBALL_URL = "oh-internal://uploads/00000000-0000-4000-8000-00000000000a"
NIGHTLY = {
    "trigger": {"type": "cron", "schedule": "0 6 * * *"},
    "prompt": "Triage open issues.\n",
    "timeout": 900,
}


class FakeAutomationService:
    """The slice of /api/automation/v1 the sync and the relay use."""

    def __init__(self) -> None:
        self.rows: dict[str, dict] = {}
        self.calls: list[httpx.Request] = []
        self.made = 0
        self.uploads: dict[str, dict] = {}

    def add(self, name: str, **fields) -> dict:
        row = {
            "id": f"id-{self.made}",
            "name": name,
            "trigger": {"type": "cron", "schedule": "0 6 * * *", "timezone": "UTC"},
            "tarball_path": TARBALL_URL,
            "timeout": 900,
            "enabled": True,
            **fields,
        }
        self.made += 1
        self.rows[row["id"]] = row
        return row

    def __call__(self, request: httpx.Request) -> httpx.Response:
        self.calls.append(request)
        assert request.headers["X-Session-API-Key"] == "automation-key"
        tail = request.url.path.removeprefix("/api/automation/v1").strip("/")
        if tail.startswith("uploads"):
            return self.upload(request, tail.removeprefix("uploads").strip("/"))
        body = json.loads(request.content) if request.content else None
        if request.method == "GET":
            rows = list(self.rows.values())
            return httpx.Response(200, json={"automations": rows, "total": len(rows)})
        if request.method == "POST" and not tail:
            return httpx.Response(201, json=self.add(body.pop("name"), **body))
        if request.method == "POST":
            return httpx.Response(200, json={"relayed": body})
        if request.method == "PATCH":
            self.rows[tail].update(body)
            return httpx.Response(200, json=self.rows[tail])
        del self.rows[tail]
        return httpx.Response(204)

    def upload(self, request: httpx.Request, upload_id: str) -> httpx.Response:
        if request.method == "GET":
            rows = list(self.uploads.values())
            return httpx.Response(200, json={"uploads": rows, "total": len(rows)})
        if request.method == "DELETE":
            del self.uploads[upload_id]
            return httpx.Response(204)
        assert request.headers["Content-Type"] == "application/gzip"
        return httpx.Response(
            201, json=self.add_upload(request.url.params["name"], TARBALL_URL)
        )

    def add_upload(self, name: str, path: str) -> dict:
        row = {"id": f"up-{len(self.uploads)}", "name": name, "tarball_path": path}
        self.uploads[row["id"]] = row
        return row

    def writes(self) -> list[str]:
        return [r.method for r in self.calls if r.method != "GET"]


def service_client(fake: FakeAutomationService) -> httpx.AsyncClient:
    return httpx.AsyncClient(transport=httpx.MockTransport(fake))


@pytest.fixture
def service(state) -> FakeAutomationService:
    fake = FakeAutomationService()
    state.automations.http = service_client(fake)
    return fake


def _define(state, **definitions) -> None:
    state.settings.automations_file.write_text(json.dumps(definitions))


@pytest.mark.anyio
async def test_sync_creates_updates_and_deletes_only_its_own(state, service):
    foreign = service.add("made in the UI", tarball_path="oh-internal://uploads/x")
    old = service.add_upload("openhands-app-server-run-old", "oh-internal://old")
    stale = service.add("removed from values", tarball_path=old["tarball_path"])
    kept = service.add("nightly", tarball_path=old["tarball_path"])
    _define(state, nightly=NIGHTLY)

    await state.automations.sync()
    # A new run script is a new upload; ours move to it, the old one goes.
    assert [u["name"] for u in service.uploads.values()] == [
        state.automations.upload_name
    ]
    assert kept["tarball_path"] == TARBALL_URL
    assert stale["id"] not in service.rows and foreign["id"] in service.rows

    del service.rows[kept["id"]]
    await state.automations.sync()
    created = next(r for r in service.rows.values() if r["name"] == "nightly")
    assert created["entrypoint"] == "python3 run.py"
    assert created["tarball_path"] == TARBALL_URL
    assert created["trigger"] == NIGHTLY["trigger"] and created["timeout"] == 900

    # The service's defaults (timezone) are not a difference.
    created["trigger"]["timezone"] = "UTC"
    service.calls.clear()
    await state.automations.sync()
    assert service.writes() == []

    _define(
        state,
        nightly={
            **NIGHTLY,
            "trigger": {"type": "cron", "schedule": "0 7 * * *"},
            "enabled": False,
        },
    )
    await state.automations.sync()
    assert service.writes() == ["PATCH"]
    assert created["trigger"]["schedule"] == "0 7 * * *" and not created["enabled"]


@pytest.mark.anyio
async def test_sync_leaves_a_disabled_automation_disabled(state, service):
    service.add_upload(state.automations.upload_name, TARBALL_URL)
    service.add("nightly", enabled=False)
    _define(state, nightly=NIGHTLY)
    await state.automations.sync()
    assert service.writes() == []


def test_tarball_runs_standalone(state):
    with tarfile.open(fileobj=io.BytesIO(state.automations.tarball)) as tar:
        assert sorted(tar.getnames()) == ["run.json", "run.py"]
        config = json.load(tar.extractfile("run.json"))
        script = tar.extractfile("run.py").read().decode()
    assert config == {
        "webhook_url": "http://app-server:8081",
        "agent_server_port": 8000,
    }
    compile(script, "run.py", "exec")
    assert "app_server" not in script.split('"""', 2)[2]


def test_a_run_starts_its_conversation_in_its_own_sandbox(api, hooks, state, user):
    _define(state, nightly=NIGHTLY)
    sandbox = api.post("/api/v1/sandboxes", headers=user).json()
    agent = FakeAgentServer()
    state.conversations.http = httpx.AsyncClient(transport=httpx.MockTransport(agent))
    path = f"/sandboxes/{sandbox['id']}/automation/conversations"
    key = {"X-Session-API-Key": sandbox["session_api_key"]}
    body = {"automation": "nightly", "event": {"action": "opened"}}

    assert hooks.post(path, json=body).status_code == 401
    assert hooks.post(path, json={"automation": "nope"}, headers=key).status_code == 404

    r = hooks.post(path, json=body, headers=key)
    assert r.status_code == 200 and r.json() == {"id": CONV_ID}
    assert agent.requests[-1].url.host.startswith(sandbox["id"])
    text = agent.body()["initial_message"]["content"][0]["text"]
    assert text.startswith("Triage open issues.\n\n## Event payload")
    assert '"action": "opened"' in text

    conv = api.get("/api/v1/app-conversations", params={"ids": CONV_ID}, headers=user)
    conv = conv.json()[0]
    assert conv["title"] == "nightly" and conv["sandbox_id"] == sandbox["id"]


def test_completion_is_relayed_with_the_services_key(api, hooks, service, user):
    hooks.app.state.http = service_client(service)
    sandbox = api.post("/api/v1/sandboxes", headers=user).json()
    run = "0cd7e625-a5f0-4164-a27c-d77ca5999e90"
    path = f"/sandboxes/{sandbox['id']}/automation/runs/{run}/complete"
    body = {"status": "COMPLETED", "conversation_id": CONV_ID}

    assert hooks.post(path, json=body).status_code == 401
    r = hooks.post(
        path, json=body, headers={"X-Session-API-Key": sandbox["session_api_key"]}
    )
    assert r.status_code == 200 and r.json() == {"relayed": body}
    assert service.calls[-1].url.path == f"/api/automation/v1/runs/{run}/complete"


def test_users_me_answers_the_automation_services_auth_check(api, user, service_auth):
    own_key = {"Authorization": "Bearer automation-key"}
    for headers in (user, service_auth, own_key):
        me = api.get("/api/v1/users/me", headers=headers)
        assert me.status_code == 200
        assert me.json()["id"] == me.json()["org_id"]
        assert "manage_automations" in me.json()["permissions"]
    wrong = {"Authorization": "Bearer nope"}
    assert api.get("/api/v1/users/me", headers=wrong).status_code == 401
    assert api.get("/api/v1/users/me").status_code == 401


def test_run_script_waits_for_the_conversation_to_stop(monkeypatch):
    from app_server import automation_run

    def outcome(*statuses: str) -> str | None:
        replies = iter(statuses)
        monkeypatch.setattr(
            automation_run, "call", lambda *a: {"execution_status": next(replies)}
        )
        monkeypatch.setattr(automation_run.time, "sleep", lambda s: None)
        return automation_run.wait("http://agent", CONV_ID, "key")

    assert outcome("idle", "running", "running", "finished") is None
    # An ACP turn can end back at idle rather than finished.
    assert outcome("running", "idle") is None
    assert outcome("running", "error") == "conversation ended error"
    assert outcome("running", "waiting_for_confirmation").endswith("confirmation")

    clock = iter(range(0, 10_000, 100))
    monkeypatch.setattr(automation_run.time, "monotonic", lambda: next(clock))
    assert outcome(*["idle"] * 5).startswith("conversation never started")
