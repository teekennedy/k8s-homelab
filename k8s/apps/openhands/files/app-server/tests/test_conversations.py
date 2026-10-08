import json
import time
import uuid

import anyio
import httpx
import pytest

CONV_ID = "0cd7e625-a5f0-4164-a27c-d77ca5999e90"
MESSAGE = {"role": "user", "content": [{"type": "text", "text": "Reply with PONG."}]}


class FakeAgentServer:
    """Answers POST /api/conversations the way agent-server 1.50 does."""

    def __init__(self, status: int = 201):
        self.status = status
        self.requests: list[httpx.Request] = []
        self.commands: list[str] = []
        self.clone_exit = 0
        # What GET /api/conversations/<id> reports.
        self.info: dict = {"execution_status": "running"}

    def __call__(self, request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/bash/execute_bash_command":
            self.commands.append(json.loads(request.content)["command"])
            return httpx.Response(
                200, json={"exit_code": self.clone_exit, "stderr": "fatal: nope"}
            )
        if request.method == "GET":
            return httpx.Response(200, json={"id": CONV_ID, **self.info})
        self.requests.append(request)
        if self.status >= 300:
            return httpx.Response(self.status, text="nope")
        return httpx.Response(
            self.status,
            json={"id": CONV_ID, "execution_status": "running", "tags": {}},
        )

    def body(self) -> dict:
        return json.loads(self.requests[-1].content)


@pytest.fixture
def agent(state, kube) -> FakeAgentServer:
    kube.auto_ready = True
    fake = FakeAgentServer()
    state.conversations.http = httpx.AsyncClient(transport=httpx.MockTransport(fake))
    return fake


def _wait_task(api, user, task_id: str) -> dict:
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        task = api.get(
            "/api/v1/app-conversations/start-tasks",
            params={"ids": task_id},
            headers=user,
        ).json()[0]
        if task["status"] in ("READY", "ERROR"):
            return task
        time.sleep(0.02)
    raise AssertionError(f"task stuck in {task['status']}")


def _start(api, user, **body) -> dict:
    task = api.post(
        "/api/v1/app-conversations",
        headers=user,
        json={"initial_message": MESSAGE, "trigger": "gui", **body},
    ).json()
    assert task["status"] == "WORKING"
    return _wait_task(api, user, task["id"])


def test_start_runs_the_conversation_in_its_own_sandbox(api, user, kube, agent):
    task = _start(api, user)
    assert task["status"] == "READY", task["detail"]
    assert task["app_conversation_id"] == CONV_ID
    sandbox_id = task["sandbox_id"]
    assert kube.sandbox(sandbox_id) is not None
    assert task["agent_server_url"] == f"https://openhands.example/runtime/{sandbox_id}"

    sent = agent.body()
    assert str(agent.requests[-1].url) == (
        f"http://{sandbox_id}.openhands.svc.cluster.local:8000/api/conversations"
    )
    # Settings with the active ACP profile laid over them.
    assert sent["agent_settings"] == {
        "agent_kind": "acp",
        "acp_server": "claude-code",
        "acp_model": "opus[1m]",
        "acp_prompt_timeout": 1800.0,
        # Declared for the server, not stored in the profile.
        "acp_command": ["claude-agent-acp"],
        "acp_args": [],
    }
    assert sent["autotitle"] is False
    assert sent["max_iterations"] == 77
    assert sent["initial_message"] == {**MESSAGE, "run": True}
    assert sent["tags"] == {"acpserver": "claude-code"}
    assert sent["workspace"] == {"working_dir": "/workspace/project"}

    conv = api.get("/api/v1/app-conversations", params={"ids": CONV_ID}, headers=user)
    conv = conv.json()[0]
    assert conv["title"] == "Reply with PONG."
    assert conv["sandbox_status"] == "RUNNING"
    assert conv["conversation_url"] == (
        f"https://openhands.example/runtime/{sandbox_id}/api/conversations/{CONV_ID}"
    )
    assert conv["session_api_key"]
    assert conv["agent_kind"] == "acp" and conv["acp_server"] == "claude-code"
    assert conv["llm_model"] == "opus[1m]"
    # No repository chosen, but git actions should still name the forge.
    assert conv["selected_repository"] is None and conv["git_provider"] == "forgejo"


def test_named_profile_overrides_the_active_one(api, user, agent):
    api.post(
        "/api/agent-profiles/fast",
        headers=user,
        json={"agent_kind": "acp", "acp_server": "claude-code", "acp_model": "sonnet"},
    )
    profiles = api.get("/api/agent-profiles", headers=user).json()["profiles"]
    fast = next(p for p in profiles if p["name"] == "fast")
    _start(api, user, agent_profile_id=fast["id"])
    assert agent.body()["agent_settings"]["acp_model"] == "sonnet"


def _profile_id(api, user, name: str) -> str:
    profiles = api.get("/api/agent-profiles", headers=user).json()["profiles"]
    return next(p["id"] for p in profiles if p["name"] == name)


def _spec_of(kube, task: dict) -> str:
    labels = kube.sandbox(task["sandbox_id"])["metadata"]["labels"]
    return labels["openhands.msng.to/sandbox-spec"]


@pytest.mark.parametrize(
    "profile, spec",
    [
        ("default", "repo"),
        ("isolated", "isolated"),
        ("isolated-sonnet", "isolated"),
        # Not a spec name followed by a dash, so not that spec.
        ("isolatedish", "repo"),
    ],
)
def test_the_agent_profile_chooses_the_sandbox_spec(
    api, user, kube, agent, profile, spec
):
    api.post(
        f"/api/agent-profiles/{profile}",
        headers=user,
        json={"agent_kind": "acp", "acp_server": "claude-code"},
    )
    task = _start(api, user, agent_profile_id=_profile_id(api, user, profile))
    assert task["status"] == "READY", task["detail"]
    assert _spec_of(kube, task) == spec


def test_no_profile_means_the_active_profiles_spec(api, user, kube, agent):
    api.post(
        f"/api/agent-profiles/{_profile_id(api, user, 'isolated')}/activate",
        headers=user,
    )
    assert _spec_of(kube, _start(api, user)) == "isolated"


REPO = {
    "selected_repository": "ops/k8s-homelab",
    "selected_branch": "main",
    "git_provider": "forgejo",
}


def test_a_selected_repository_is_cloned_before_the_conversation(api, user, agent):
    task = _start(api, user, **REPO)
    assert task["status"] == "READY", task["detail"]
    (command,) = agent.commands
    assert "https://forge.example/ops/k8s-homelab.git" in command
    assert "git checkout -q -B main --track origin/main" in command
    conv = api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert conv["selected_repository"] == "ops/k8s-homelab"
    assert conv["selected_branch"] == "main" and conv["git_provider"] == "forgejo"


def test_a_failed_clone_fails_the_start_and_removes_the_sandbox(api, user, kube, agent):
    agent.clone_exit = 128
    task = _start(api, user, **REPO)
    assert task["status"] == "ERROR"
    assert "ops/k8s-homelab" in task["detail"] and "fatal: nope" in task["detail"]
    assert not agent.requests
    assert kube.sandbox(task["sandbox_id"]) is None


def test_refused_start_reports_error_and_removes_the_sandbox(api, user, kube, agent):
    agent.status = 422
    task = _start(api, user)
    assert task["status"] == "ERROR"
    assert "422" in task["detail"]
    assert kube.sandbox(task["sandbox_id"]) is None


def test_unfinished_tasks_are_failed_after_a_restart(api, user, state, kube):
    sandbox = anyio.run(state.sandboxes.create, "repo", "alice")
    state.db.run(
        "INSERT INTO start_tasks (id, status, request, created_by, created_at,"
        " updated_at, sandbox_id) VALUES ('t1', 'WAITING_FOR_SANDBOX', '{}', 'a',"
        " 'x', 'x', ?)",
        sandbox["id"],
    )
    anyio.run(state.conversations.abandon_unfinished_tasks)
    task = api.get(
        "/api/v1/app-conversations/start-tasks", params={"ids": "t1"}, headers=user
    ).json()[0]
    assert task["status"] == "ERROR" and "restarted" in task["detail"]
    assert kube.sandbox(sandbox["id"]) is None


def _event(i: int, kind: str = "MessageEvent") -> dict:
    return {
        "id": f"e{i}",
        "timestamp": f"2026-10-01T06:34:{i:02d}.000000",
        "kind": kind,
        "source": "agent",
    }


def _post_events(hooks, api, user, sandbox_id: str, events: list[dict]):
    key = api.get("/api/v1/sandboxes", params={"id": sandbox_id}, headers=user)
    key = key.json()[0]["session_api_key"]
    r = hooks.post(
        f"/sandboxes/{sandbox_id}/events/{uuid.UUID(CONV_ID).hex}",
        json=events,
        headers={"X-Session-API-Key": key},
    )
    assert r.status_code == 200
    return key


def test_history_is_kept_and_paged(api, hooks, user, agent):
    sandbox_id = _start(api, user)["sandbox_id"]
    _post_events(hooks, api, user, sandbox_id, [_event(i) for i in range(5)])
    # Redelivery after a webhook retry is not a duplicate.
    _post_events(hooks, api, user, sandbox_id, [_event(3), _event(4)])

    path = f"/api/v1/conversation/{CONV_ID}/events"
    assert api.get(f"{path}/count", headers=user).json() == 5
    page = api.get(
        f"{path}/search",
        params={"limit": 2, "sort_order": "TIMESTAMP_DESC"},
        headers=user,
    ).json()
    assert [e["id"] for e in page["items"]] == ["e4", "e3"]
    rest = api.get(
        f"{path}/search",
        params={
            "limit": 2,
            "sort_order": "TIMESTAMP_DESC",
            "page_id": page["next_page_id"],
        },
        headers=user,
    ).json()
    assert [e["id"] for e in rest["items"]] == ["e2", "e1"]

    window = api.get(
        f"{path}/search",
        params={
            "timestamp__gte": "2026-10-01T06:34:01Z",
            "timestamp__lt": "2026-10-01T06:34:03+00:00",
        },
        headers=user,
    ).json()
    assert [e["id"] for e in window["items"]] == ["e1", "e2"]


def test_history_survives_the_sandbox(api, hooks, user, agent):
    sandbox_id = _start(api, user)["sandbox_id"]
    _post_events(hooks, api, user, sandbox_id, [_event(1)])
    api.delete(f"/api/v1/sandboxes/{sandbox_id}", headers=user)

    conv = api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert conv["sandbox_status"] == "MISSING"
    assert conv["conversation_url"] is None and conv["session_api_key"] is None
    items = api.get(
        f"/api/v1/conversation/{CONV_ID}/events/search", headers=user
    ).json()["items"]
    assert [e["id"] for e in items] == ["e1"]


def test_another_sandbox_cannot_write_this_history(api, hooks, user, agent):
    _start(api, user)
    other = api.post("/api/v1/sandboxes", headers=user).json()
    r = hooks.post(
        f"/sandboxes/{other['id']}/events/{uuid.UUID(CONV_ID).hex}",
        json=[_event(1)],
        headers={"X-Session-API-Key": other["session_api_key"]},
    )
    assert r.status_code == 200
    assert (
        api.get(f"/api/v1/conversation/{CONV_ID}/events/count", headers=user).json()
        == 0
    )


def test_conversation_webhook_updates_status(api, hooks, user, agent):
    sandbox_id = _start(api, user)["sandbox_id"]
    key = _post_events(hooks, api, user, sandbox_id, [])
    hooks.post(
        f"/sandboxes/{sandbox_id}/conversations",
        json={"id": CONV_ID, "execution_status": "finished", "metrics": {"x": 1}},
        headers={"X-Session-API-Key": key},
    )
    conv = api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert conv["execution_status"] == "finished" and conv["metrics"] == {"x": 1}


def test_refresh_reads_status_and_usage_from_running_sandboxes(
    api, user, state, kube, agent
):
    task = _start(api, user)
    stats = {"usage_to_metrics": {"acp-managed": {"accumulated_cost": 0.5}}}
    agent.info = {"execution_status": "finished", "stats": stats}

    anyio.run(state.conversations.refresh)
    row = state.conversations.row(CONV_ID)
    meta = json.loads(row["meta"])
    assert meta["execution_status"] == "finished" and meta["stats"] == stats

    # Nothing new: the conversation is not counted as updated again.
    anyio.run(state.conversations.refresh)
    assert state.conversations.row(CONV_ID)["updated_at"] == row["updated_at"]

    # A suspended sandbox has no server to ask.
    agent.info = {"execution_status": "running"}
    anyio.run(state.sandboxes.pause, task["sandbox_id"])
    anyio.run(state.conversations.refresh)
    assert state.conversations.row(CONV_ID)["updated_at"] == row["updated_at"]


def test_search_count_patch_delete(api, hooks, user, kube, agent):
    sandbox_id = _start(api, user)["sandbox_id"]
    _post_events(hooks, api, user, sandbox_id, [_event(1)])
    search = api.get("/api/v1/app-conversations/search", headers=user).json()
    assert [c["id"] for c in search["items"]] == [CONV_ID]
    assert api.get("/api/v1/app-conversations/count", headers=user).json() == 1

    r = api.patch(
        f"/api/v1/app-conversations/{CONV_ID}",
        headers=user,
        json={"title": "Renamed", "public": True},
    )
    assert r.json()["title"] == "Renamed" and r.json()["public"] is True

    assert api.delete(f"/api/v1/app-conversations/{CONV_ID}", headers=user).json()
    assert kube.sandbox(sandbox_id) is None
    assert (
        api.get(f"/api/v1/app-conversations/{CONV_ID}", headers=user).status_code == 404
    )
    assert api.get("/api/v1/app-conversations/count", headers=user).json() == 0
    assert api.get(
        "/api/v1/app-conversations", params={"ids": CONV_ID}, headers=user
    ).json() == [None]
