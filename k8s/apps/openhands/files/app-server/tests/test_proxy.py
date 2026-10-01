import uuid

import httpx

from app_server.proxy import upstream_headers


def test_upstream_headers_strip_this_origins_credentials():
    out = upstream_headers(
        {
            "Cookie": "_oauth2_proxy_openhands=secret",
            "Authorization": "Bearer ohk_x",
            "X-Forwarded-User": "alice",
            "X-Forwarded-For": "10.0.0.1",
            "Host": "openhands.example",
            "Content-Length": "3",
            "X-Session-API-Key": "k",
            "Accept": "application/json",
        },
        "X-Forwarded-User",
    )
    assert out == {"X-Session-API-Key": "k", "Accept": "application/json"}


def _stub_upstream(state, seen):
    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        # A real stream: the proxy relays it with aiter_raw.
        return httpx.Response(200, stream=httpx.ByteStream(request.url.path.encode()))

    state.http = httpx.AsyncClient(transport=httpx.MockTransport(handler))
    return state


def test_proxy_strips_prefix_and_forwards(api, state, user):
    sid = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    seen = []
    api.app.state.http = _stub_upstream(state, seen).http

    r = api.get(f"/runtime/{sid}/api/conversations/abc?x=1", headers=user)
    assert r.status_code == 200
    req = seen[0]
    assert str(req.url) == (
        f"http://{sid}.openhands.svc.cluster.local:8000/api/conversations/abc?x=1"
    )
    assert "cookie" not in req.headers and "x-forwarded-user" not in req.headers


def test_proxy_accepts_session_key_without_user(api, state, user):
    info = api.post("/api/v1/sandboxes", headers=user).json()
    seen = []
    api.app.state.http = _stub_upstream(state, seen).http
    sid = info["id"]

    assert api.get(f"/runtime/{sid}/alive").status_code == 401
    wrong = {"X-Session-API-Key": "nope"}
    assert api.get(f"/runtime/{sid}/alive", headers=wrong).status_code == 401
    right = {"X-Session-API-Key": info["session_api_key"]}
    assert api.get(f"/runtime/{sid}/alive", headers=right).status_code == 200


def test_proxy_refuses_unminted_and_deleted(api, user):
    assert api.get("/runtime/sbx-nope/alive", headers=user).status_code == 404
    sid = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    api.delete(f"/api/v1/sandboxes/{sid}", headers=user)
    assert api.get(f"/runtime/{sid}/alive", headers=user).status_code == 404


def test_webhook_requires_the_sandboxes_own_key(api, hooks, user):
    a = api.post("/api/v1/sandboxes", headers=user).json()
    b = api.post("/api/v1/sandboxes", headers=user).json()
    path = f"/sandboxes/{a['id']}/events/{uuid.uuid4().hex}"
    assert hooks.post(path, json=[]).status_code == 401
    wrong = {"X-Session-API-Key": b["session_api_key"]}
    assert hooks.post(path, json=[], headers=wrong).status_code == 401
    right = {"X-Session-API-Key": a["session_api_key"]}
    assert hooks.post(path, json=[], headers=right).status_code == 200
