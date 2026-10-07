import httpx


def test_settings_are_seeded_then_merged(api, user):
    got = api.get("/api/v1/settings", headers=user).json()
    assert got["agent_settings"] == {"agent_kind": "acp", "acp_server": "claude-code"}
    assert got["conversation_settings"] == {"max_iterations": 77}
    assert got["language"] == "en"

    api.post(
        "/api/v1/settings",
        headers=user,
        json={
            "agent_settings_diff": {"acp_model": "sonnet", "llm": {"model": "m"}},
            "conversation_settings_diff": {"confirmation_mode": True},
            "language": "de",
            "enable_sound_notifications": True,
        },
    )
    got = api.get("/api/v1/settings", headers=user).json()
    assert got["agent_settings"] == {
        "agent_kind": "acp",
        "acp_server": "claude-code",
        "acp_model": "sonnet",
        "llm": {"model": "m"},
    }
    assert got["conversation_settings"] == {
        "max_iterations": 77,
        "confirmation_mode": True,
    }
    assert got["language"] == "de" and got["enable_sound_notifications"] is True


def test_llm_key_is_write_only(api, user, state):
    api.post(
        "/api/v1/settings",
        headers=user,
        json={"agent_settings_diff": {"llm": {"model": "m", "api_key": "sk-1"}}},
    )
    got = api.get("/api/v1/settings", headers=user).json()
    assert got["llm_api_key_set"] is True
    assert got["agent_settings"]["llm"]["api_key"] is None
    # Saving the form back sends the null it was shown; the key survives.
    api.post(
        "/api/v1/settings",
        headers=user,
        json={"agent_settings_diff": {"llm": {"model": "m2", "api_key": None}}},
    )
    stored = state.settings_store.settings()["agent_settings"]["llm"]
    assert stored == {"model": "m2", "api_key": "sk-1"}


def test_schemas_come_from_the_dumped_files(api, user):
    r = api.get("/api/v1/settings/agent-schema", headers=user)
    assert r.json() == {"model_name": "AgentSettings"}
    assert (
        api.get("/api/v1/settings/conversation-schema", headers=user).status_code == 503
    )


def test_agent_profiles_lifecycle(api, user):
    listing = api.get("/api/agent-profiles", headers=user).json()
    # The seeded profile, and one for the spec that is not the default.
    default, isolated = listing["profiles"]
    assert isolated["name"] == "isolated"
    assert default["name"] == "default" and default["agent_kind"] == "acp"
    assert listing["active_agent_profile_id"] == default["id"]
    # Seeded once: the id is stable across reads.
    again = api.get("/api/agent-profiles", headers=user).json()
    assert again["active_agent_profile_id"] == default["id"]

    detail = api.get("/api/agent-profiles/default", headers=user).json()
    assert detail["profile"]["acp_model"] == "opus[1m]"
    assert detail["profile"]["revision"] == 0

    api.post(
        "/api/agent-profiles/default",
        headers=user,
        json={"agent_kind": "acp", "acp_server": "claude-code", "acp_model": "sonnet"},
    )
    detail = api.get("/api/agent-profiles/default", headers=user).json()["profile"]
    assert detail["id"] == default["id"] and detail["revision"] == 1

    assert (
        api.post(
            "/api/agent-profiles/x", headers=user, json={"agent_kind": "openhands"}
        ).status_code
        == 422
    )
    api.post("/api/agent-profiles/other", headers=user, json={"agent_kind": "acp"})
    assert (
        api.post(
            "/api/agent-profiles/other/rename",
            headers=user,
            json={"new_name": "default"},
        ).status_code
        == 409
    )
    api.post("/api/agent-profiles/other/rename", headers=user, json={"new_name": "o2"})
    o2 = api.get("/api/agent-profiles/o2", headers=user).json()["profile"]
    api.post(f"/api/agent-profiles/{o2['id']}/activate", headers=user)
    assert (
        api.get("/api/agent-profiles", headers=user).json()["active_agent_profile_id"]
        == o2["id"]
    )
    api.delete("/api/agent-profiles/o2", headers=user)
    listing = api.get("/api/agent-profiles", headers=user).json()
    assert listing["active_agent_profile_id"] is None
    assert [p["name"] for p in listing["profiles"]] == ["default", "isolated"]


def test_account_stubs(api, user):
    orgs = api.get("/api/organizations", headers=user).json()
    (org,) = orgs["items"]
    assert orgs["current_org_id"] == org["id"] and org["is_personal"]
    me = api.get(f"/api/organizations/{org['id']}/me", headers=user).json()
    assert me["org_id"] == me["user_id"] == org["id"]
    assert api.get("/api/keys/current", headers=user).json()["org_id"] == org["id"]
    page = api.get("/api/v1/git/installations/search", headers=user).json()
    assert page == {"items": [], "next_page_id": None}
    assert api.get("/api/organizations", headers={}).status_code == 401


def test_automation_is_reached_with_the_service_key(api, user, state):
    seen = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        return httpx.Response(200, stream=httpx.ByteStream(b"[]"))

    api.app.state.http = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    r = api.get(
        "/api/automation/v1?limit=50",
        headers={**user, "Cookie": "_oauth2_proxy_openhands=x"},
    )
    assert r.status_code == 200
    req = seen[0]
    assert str(req.url) == "http://canvas:18001/api/automation/v1?limit=50"
    assert req.headers["X-Session-API-Key"] == "automation-key"
    assert "cookie" not in req.headers
    assert api.get("/api/automation/v1").status_code == 401
