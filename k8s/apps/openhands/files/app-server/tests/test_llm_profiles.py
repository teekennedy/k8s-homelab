import json

import httpx
import pytest

from app_server.settings_store import ProfileError, SettingsStore


def test_declared_codex_agents_survive_restart_and_ui_edits(tmp_path):
    from app_server.db import Database

    seed = tmp_path / "seed.json"
    seed.write_text(
        json.dumps(
            {
                "agent_settings": {"agent_kind": "acp", "acp_server": "claude-code"},
                "llm_profiles": {"codex": {"model": "gpt-5.5"}},
                "agent_profiles": {
                    "isolated-codex": {
                        "agent_kind": "openhands",
                        "llm_profile_ref": "codex",
                    }
                },
            }
        )
    )
    db = Database(tmp_path / "db")
    store = SettingsStore(db, seed)
    store.ensure_declared_profiles()
    profile = store.get_profile("isolated-codex")["profile"]
    store.save_llm_profile("codex", {"model": "gpt-6-astra"})
    store = SettingsStore(db, seed)
    store.ensure_declared_profiles()
    assert store.get_profile("isolated-codex")["profile"]["id"] == profile["id"]
    agent = store.resolve_agent(profile["id"])
    assert agent == {
        "agent_kind": "openhands",
        "llm": {
            "model": "gpt-6-astra",
            "auth_type": "subscription",
            "subscription_vendor": "openai",
            "stream": True,
        },
    }
    db.close()


@pytest.mark.parametrize(
    "base",
    [
        "/api/v1/settings/profiles",
        "/api/organizations/personal/profiles",
    ],
)
def test_llm_profile_crud_and_selection(api, user, state, base):
    assert api.get(base).status_code == 401
    assert (
        api.post(
            f"{base}/codex",
            headers=user,
            json={
                "llm": {"model": "gpt-5.5", "api_key": "secret"},
            },
        ).status_code
        == 422
    )
    assert (
        api.post(
            f"{base}/codex",
            headers=user,
            json={
                "llm": {"model": "gpt-5.5", "base_url": "https://example.com"},
            },
        ).status_code
        == 200
    )
    api.post(
        "/api/agent-profiles/codex",
        headers=user,
        json={
            "agent_kind": "openhands",
            "llm_profile_ref": "codex",
        },
    )
    api.post(f"{base}/fast", headers=user, json={"llm": {"model": "gpt-5.6-luna"}})
    api.post(f"{base}/fast/activate", headers=user)
    profile = api.get("/api/agent-profiles/codex", headers=user).json()["profile"]
    assert (
        state.settings_store.resolve_agent(profile["id"])["llm"]["model"]
        == "gpt-5.6-luna"
    )
    assert state.settings_store.resolve_agent(None)["agent_kind"] == "acp"
    assert "base_url" not in api.get(f"{base}/codex", headers=user).json()["config"]
    # Cloud Canvas gates its composer on this backend-auth readiness signal.
    summary = api.get(base, headers=user).json()["profiles"][0]
    assert summary["api_key_set"] is True
    assert "api_key" not in api.get(f"{base}/codex", headers=user).json()["config"]
    assert api.delete(f"{base}/codex", headers=user).status_code == 409
    assert (
        api.post(
            f"{base}/codex/rename", headers=user, json={"new_name": "renamed"}
        ).status_code
        == 200
    )
    assert (
        api.get("/api/agent-profiles/codex", headers=user).json()["profile"][
            "llm_profile_ref"
        ]
        == "renamed"
    )
    assert api.delete(f"{base}/fast", headers=user).status_code == 200
    assert api.get(base, headers=user).json()["active_profile"] is None
    assert api.get(f"{base}/missing", headers=user).status_code == 404
    with pytest.raises(ProfileError):
        state.settings_store.save_profile(
            "broken", {"agent_kind": "openhands", "llm_profile_ref": "missing"}
        )


def test_switch_llm_uses_sandbox_auth_and_updates_metadata(api, user, state, kube):
    from tests.test_conversations import CONV_ID, FakeAgentServer, _start

    kube.auto_ready = True
    agent = FakeAgentServer()
    state.conversations.http = httpx.AsyncClient(transport=httpx.MockTransport(agent))
    store = state.settings_store
    store.save_llm_profile("codex", {"model": "gpt-5.5"})
    store.save_profile(
        "isolated-codex", {"agent_kind": "openhands", "llm_profile_ref": "codex"}
    )
    profile = store.get_profile("isolated-codex")["profile"]
    task = _start(api, user, agent_profile_id=profile["id"])
    assert task["status"] == "READY"
    assert (
        kube.sandbox(task["sandbox_id"])["metadata"]["labels"][
            "openhands.msng.to/sandbox-spec"
        ]
        == "isolated"
    )
    assert agent.body()["agent_settings"]["agent_kind"] == "openhands"
    assert "tags" not in agent.body()
    seen = []

    def handler(request):
        seen.append(request)
        return httpx.Response(200, json={"success": True})

    state.http = api.app.state.http = httpx.AsyncClient(
        transport=httpx.MockTransport(handler)
    )
    store.save_llm_profile("fast", {"model": "gpt-5.6-luna"})
    path = f"/api/v1/app-conversations/{CONV_ID}/switch_profile"
    assert (
        api.post(path, headers=user, json={"profile_name": "fast"}).status_code == 200
    )
    assert seen[0].url.path == f"/api/conversations/{CONV_ID}/switch_llm"
    assert seen[0].headers["X-Session-API-Key"]
    assert json.loads(seen[0].content)["llm"]["auth_type"] == "subscription"
    assert (
        json.loads(state.conversations.row(CONV_ID)["meta"])["llm_model"]
        == "gpt-5.6-luna"
    )
    # Failed swaps leave the recorded model intact.
    api.app.state.http = httpx.AsyncClient(
        transport=httpx.MockTransport(
            lambda _: httpx.Response(400, text="unsupported model")
        )
    )
    assert (
        api.post(path, headers=user, json={"profile_name": "codex"}).status_code == 400
    )
    assert (
        json.loads(state.conversations.row(CONV_ID)["meta"])["llm_model"]
        == "gpt-5.6-luna"
    )
