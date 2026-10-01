import json

import anyio
import pytest

from app_server.db import now
from app_server.sandboxes import MANAGED_BY, MANAGED_BY_VALUE, derive_status, render


def test_render_substitutes_id_and_labels():
    tpl = {"metadata": {"labels": {"a": "b"}}, "spec": {"x": "__SANDBOX_ID__-config"}}
    out = render(tpl, "sbx-1")
    assert out["metadata"]["name"] == "sbx-1"
    assert out["metadata"]["labels"][MANAGED_BY] == MANAGED_BY_VALUE
    assert out["spec"]["x"] == "sbx-1-config"
    assert tpl["spec"]["x"] == "__SANDBOX_ID__-config", "template must not be mutated"


@pytest.mark.parametrize(
    "cr, pod, expected",
    [
        (None, None, "MISSING"),
        ({"spec": {"operatingMode": "Running"}}, None, "STARTING"),
        (
            {
                "spec": {"operatingMode": "Running"},
                "status": {"conditions": [{"type": "Ready", "status": "True"}]},
            },
            {"status": {"phase": "Running"}},
            "RUNNING",
        ),
        ({"spec": {"operatingMode": "Suspended"}}, None, "PAUSED"),
        (
            {"spec": {"operatingMode": "Running"}},
            {"status": {"phase": "Failed"}},
            "ERROR",
        ),
    ],
)
def test_derive_status(cr, pod, expected):
    assert derive_status(cr, pod) == expected


def test_create_writes_sandbox_and_owned_secret(api, kube, user):
    r = api.post("/api/v1/sandboxes", headers=user)
    assert r.status_code == 200, r.text
    info = r.json()
    sid = info["id"]
    assert info["status"] == "STARTING"
    assert info["sandbox_spec_id"] == "repo"
    assert info["exposed_urls"] is None

    sandbox = kube.sandbox(sid)
    env = sandbox["spec"]["podTemplate"]["spec"]["containers"][0]["env"][0]
    assert env["valueFrom"]["secretKeyRef"]["name"] == f"{sid}-config"

    secret = kube.objects["secrets"][f"{sid}-config"]
    owner = secret["metadata"]["ownerReferences"][0]
    assert owner["uid"] == sandbox["metadata"]["uid"]
    assert secret["stringData"]["session-api-key"] == info["session_api_key"]
    config = json.loads(secret["stringData"]["config.json"])
    assert (
        config["webhooks"][0]["base_url"] == f"http://app-server:8081/sandboxes/{sid}"
    )


def test_create_rolls_back_when_secret_fails(api, kube, state, user):
    kube.fail_create.add("secrets")
    with pytest.raises(Exception):
        api.post("/api/v1/sandboxes", headers=user)
    assert not kube.objects.get("sandboxes")
    assert state.db.all("SELECT * FROM sandboxes WHERE deleted_at IS NULL") == []


def test_unknown_spec_is_400(api, user):
    r = api.post("/api/v1/sandboxes", headers=user, json={"sandbox_spec_id": "nope"})
    assert r.status_code == 400


def test_running_sandbox_url_depends_on_caller(api, kube, user, service_auth):
    sid = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    kube.set_ready(sid)

    browser = api.get("/api/v1/sandboxes", params={"id": sid}, headers=user).json()[0]
    assert browser["status"] == "RUNNING"
    assert browser["exposed_urls"] == [
        {"name": "AGENT_SERVER", "url": f"https://openhands.example/runtime/{sid}"}
    ]

    automation = api.get(
        "/api/v1/sandboxes", params={"id": sid}, headers=service_auth
    ).json()[0]
    assert (
        automation["exposed_urls"][0]["url"] == f"http://app-server:8080/runtime/{sid}"
    )


def test_batch_get_keeps_positions_for_unknown_ids(api, user):
    sid = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    got = api.get(
        "/api/v1/sandboxes", params=[("id", "nope"), ("id", sid)], headers=user
    ).json()
    assert got[0] is None and got[1]["id"] == sid


def test_pause_resume_delete(api, kube, user):
    sid = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    assert api.post(f"/api/v1/sandboxes/{sid}/pause", headers=user).status_code == 200
    assert kube.sandbox(sid)["spec"]["operatingMode"] == "Suspended"
    info = api.get("/api/v1/sandboxes", params={"id": sid}, headers=user).json()[0]
    assert info["status"] == "PAUSED"

    assert api.post(f"/api/v1/sandboxes/{sid}/resume", headers=user).status_code == 200
    assert kube.sandbox(sid)["spec"]["operatingMode"] == "Running"

    r = api.delete(f"/api/v1/sandboxes/{sid}", params={"sandbox_id": sid}, headers=user)
    assert r.status_code == 200
    assert kube.sandbox(sid) is None
    assert f"{sid}-config" not in kube.objects["secrets"]
    info = api.get("/api/v1/sandboxes", params={"id": sid}, headers=user).json()[0]
    assert info["status"] == "MISSING" and info["session_api_key"] is None
    assert api.delete(f"/api/v1/sandboxes/{sid}", headers=user).status_code == 404


def test_search_pages(api, user):
    ids = {api.post("/api/v1/sandboxes", headers=user).json()["id"] for _ in range(3)}
    first = api.get("/api/v1/sandboxes/search", params={"limit": 2}, headers=user)
    body = first.json()
    assert len(body["items"]) == 2 and body["next_page_id"] == "2"
    rest = api.get(
        "/api/v1/sandboxes/search",
        params={"limit": 2, "page_id": body["next_page_id"]},
        headers=user,
    ).json()
    assert rest["next_page_id"] is None
    assert {i["id"] for i in body["items"] + rest["items"]} == ids


def test_reconcile_deletes_orphans_and_marks_vanished(api, kube, state, user):
    orphan = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    vanished = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    in_flight = api.post("/api/v1/sandboxes", headers=user).json()["id"]
    state.db.run("UPDATE sandboxes SET deleted_at = ? WHERE id = ?", now(), orphan)
    del kube.objects["sandboxes"][vanished]
    # Mid-create: the row is written but the Sandbox is not, so no uid yet.
    del kube.objects["sandboxes"][in_flight]
    state.db.run("UPDATE sandboxes SET uid = NULL WHERE id = ?", in_flight)

    anyio.run(state.sandboxes.reconcile)

    assert kube.sandbox(orphan) is None
    assert state.sandboxes.live_row(vanished) is None
    assert state.sandboxes.live_row(in_flight) is not None


def test_api_requires_identity(api):
    assert api.get("/api/v1/sandboxes/search").status_code == 401
    bad = {"Authorization": "Bearer nope"}
    assert api.get("/api/v1/sandboxes/search", headers=bad).status_code == 401


def test_mint_requires_service_key(api):
    path = "/api/service/users/u/orgs/o/api-keys"
    assert api.post(path).status_code == 401
    assert api.post(path, headers={"X-Service-API-Key": "wrong"}).status_code == 401
