import json

from tests.test_conversations import _start
from tests.test_runtime import runtime  # noqa: F401  (fixture)


def test_secret_crud(api, user):
    assert (
        api.post(
            "/api/v1/secrets",
            headers=user,
            json={"name": "GH_TOKEN", "value": "s3cr3t"},
        ).status_code
        == 200
    )
    assert (
        api.post(
            "/api/v1/secrets", headers=user, json={"name": "GH_TOKEN", "value": "x"}
        ).status_code
        == 409
    )
    assert (
        api.post(
            "/api/v1/secrets", headers=user, json={"name": "1BAD", "value": "x"}
        ).status_code
        == 422
    )

    page = api.get("/api/v1/secrets/search", headers=user).json()
    assert page == {
        "items": [{"name": "GH_TOKEN", "description": None}],
        "next_page_id": None,
    }

    api.put(
        "/api/v1/secrets/GH_TOKEN",
        headers=user,
        json={"name": "FORGE_TOKEN", "description": "forge"},
    )
    page = api.get("/api/v1/secrets/search", headers=user).json()
    assert page["items"] == [{"name": "FORGE_TOKEN", "description": "forge"}]

    assert api.delete("/api/v1/secrets/FORGE_TOKEN", headers=user).status_code == 200
    assert api.delete("/api/v1/secrets/FORGE_TOKEN", headers=user).status_code == 404


def test_values_are_encrypted_at_rest(api, user, state):
    api.post("/api/v1/secrets", headers=user, json={"name": "K", "value": "plaintext"})
    (row,) = state.db.all("SELECT value FROM secrets")
    assert b"plaintext" not in row["value"]


def test_secrets_reach_new_conversations(api, user, runtime):
    api.post(
        "/api/v1/secrets",
        headers=user,
        json={"name": "GH_TOKEN", "value": "s3cr3t", "description": "d"},
    )
    _start(api, user)
    sent = json.loads(runtime.last("/api/conversations").content)
    assert sent["secrets"] == {
        "GH_TOKEN": {"kind": "StaticSecret", "value": "s3cr3t", "description": "d"}
    }


def test_login_returns_to_the_page_that_lost_its_session(api, user):
    def location(path, **params):
        r = api.get(path, headers=user, params=params, follow_redirects=False)
        assert r.status_code == 302
        return r.headers["location"]

    back = "/canvas/conversations/abc?backend=locked-cloud&org=1"
    assert location("/canvas/login", returnTo=back) == back
    assert location("/login", returnTo=back) == back
    assert location("/canvas/login") == "/canvas/"
    for elsewhere in (
        "https://example.com/",
        "//example.com/",
        "/\\example.com/",
        "canvas",
        "/canvas/login?returnTo=%2Fcanvas%2Flogin",
        "/login/",
    ):
        assert location("/canvas/login", returnTo=elsewhere) == "/canvas/"
    assert api.get("/canvas/login", follow_redirects=False).status_code == 401


def test_session_stubs(api, user):
    assert api.post("/api/authenticate", headers=user).status_code == 200
    assert api.post("/api/authenticate").status_code == 401
    assert api.post("/api/analytics/events", headers=user, json={}).status_code == 204
    assert api.get("/api/v1/settings/profiles", headers=user).json() == {
        "profiles": [],
        "active_profile": None,
    }
