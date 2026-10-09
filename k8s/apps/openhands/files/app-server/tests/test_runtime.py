import json

import httpx
import pytest

from tests.test_conversations import CONV_ID, _start


class FakeRuntime:
    """The agent-server calls the delegating endpoints make."""

    def __init__(self):
        self.requests: list[httpx.Request] = []

    def __call__(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path = request.url.path
        if path == "/api/conversations":
            return httpx.Response(201, json={"id": CONV_ID, "tags": {}})
        if path == "/api/skills":
            return httpx.Response(200, json={"skills": [{"name": "forgejo-iterate"}]})
        if path == "/api/bash/execute_bash_command":
            out = "./README.md\n./src/app.py\n./README.md\n"
            return httpx.Response(200, json={"exit_code": 0, "stdout": out})
        if path == "/api/file/download":
            return httpx.Response(200, text="hello\n")
        if path == "/api/git/changes":
            return httpx.Response(200, json=[{"status": "MODIFIED", "path": "a.py"}])
        if path == "/api/git/diff":
            return httpx.Response(200, json={"modified": "b", "original": "a"})
        if path.endswith("/switch_acp_model"):
            return httpx.Response(204)
        if path.startswith("/api/file/download-trajectory/"):
            return httpx.Response(200, stream=httpx.ByteStream(b"PK zip"))
        return httpx.Response(404)

    def last(self, path: str) -> httpx.Request:
        return next(r for r in reversed(self.requests) if r.url.path == path)


@pytest.fixture
def runtime(api, state, kube) -> FakeRuntime:
    kube.auto_ready = True
    fake = FakeRuntime()
    client = httpx.AsyncClient(transport=httpx.MockTransport(fake))
    state.conversations.http = client
    api.app.state.http = client
    return fake


BASE = f"/api/v1/app-conversations/{CONV_ID}"


def test_delegates_to_the_conversations_own_agent_server(api, user, runtime):
    sandbox_id = _start(api, user)["sandbox_id"]
    agent = f"{sandbox_id}.openhands.svc.cluster.local"

    assert api.get(f"{BASE}/skills", headers=user).json() == {
        "skills": [{"name": "forgejo-iterate"}]
    }
    skills_req = runtime.last("/api/skills")
    assert skills_req.url.host == agent
    assert json.loads(skills_req.content)["project_dir"] == "/workspace/project"

    files = api.get(
        f"{BASE}/files", params={"path": "/workspace/project"}, headers=user
    )
    assert files.json() == ["README.md", "src/app.py"]
    bash = json.loads(runtime.last("/api/bash/execute_bash_command").content)
    assert bash["cwd"] == "/workspace/project" and "-prune" in bash["command"]

    file = api.get(
        f"{BASE}/file",
        params={"file_path": "/workspace/project/README.md"},
        headers=user,
    )
    assert file.text == "hello\n"
    assert runtime.last("/api/file/download").url.params["path"] == (
        "/workspace/project/README.md"
    )

    changes = api.get(f"{BASE}/git/changes", params={"path": "/w"}, headers=user)
    assert changes.json() == [{"status": "MODIFIED", "path": "a.py"}]
    assert runtime.last("/api/git/changes").url.params["ref"] == "HEAD"
    diff = api.get(f"{BASE}/git/diff", params={"path": "a.py"}, headers=user)
    assert diff.json() == {"modified": "b", "original": "a"}
    assert runtime.last("/api/git/diff").url.params["ref"] == "HEAD"

    assert api.get(f"{BASE}/download", headers=user).content == b"PK zip"

    r = api.post(f"{BASE}/switch_acp_model", headers=user, json={"model": "sonnet"})
    assert r.status_code == 200
    assert api.get(BASE, headers=user).json()["llm_model"] == "sonnet"

    # Every call carried the sandbox's key, never the browser's identity.
    for req in runtime.requests:
        assert req.headers["X-Session-API-Key"]
        assert "x-forwarded-user" not in req.headers


def test_without_a_running_sandbox(api, user, runtime):
    sandbox_id = _start(api, user)["sandbox_id"]
    api.delete(f"/api/v1/sandboxes/{sandbox_id}", headers=user)
    seen = len(runtime.requests)

    assert api.get(f"{BASE}/skills", headers=user).json() == {"skills": []}
    assert api.get(f"{BASE}/files", headers=user).json() == []
    assert api.get(f"{BASE}/git/changes", headers=user).json() == []
    assert (
        api.get(f"{BASE}/file", params={"file_path": "x"}, headers=user).status_code
        == 404
    )
    assert (
        api.get(f"{BASE}/git/diff", params={"path": "x"}, headers=user).status_code
        == 404
    )
    r = api.post(f"{BASE}/switch_acp_model", headers=user, json={"model": "m"})
    assert r.status_code == 409
    assert len(runtime.requests) == seen, "nothing should reach a sandbox that is gone"


def test_unknown_conversation_is_404(api, user, runtime):
    assert (
        api.get("/api/v1/app-conversations/nope/skills", headers=user).status_code
        == 404
    )


def test_llm_profile_switch_is_refused(api, user, runtime):
    _start(api, user)
    r = api.post(f"{BASE}/switch_profile", headers=user, json={"profile_name": "x"})
    assert r.status_code == 409
