import json
import uuid
from datetime import UTC, datetime
from typing import Any

import pytest
from fastapi.testclient import TestClient

from app_server.db import Database
from app_server.kube import KubeError, Resource
from app_server.main import State, build_api, build_webhooks
from app_server.settings import Settings

SERVICE_KEY = "test-service-key"
USER_HEADER = "X-Forwarded-User"

SPEC = {
    "metadata": {"labels": {"openhands.msng.to/sandbox-spec": "repo"}},
    "spec": {
        "service": True,
        "operatingMode": "Running",
        "podTemplate": {
            "spec": {
                "containers": [
                    {
                        "name": "agent-server",
                        "env": [
                            {
                                "name": "OH_SESSION_API_KEYS_0",
                                "valueFrom": {
                                    "secretKeyRef": {
                                        "name": "__SANDBOX_ID__-config",
                                        "key": "session-api-key",
                                    }
                                },
                            }
                        ],
                    }
                ]
            }
        },
    },
}


class FakeKube:
    """In-memory stand-in for the four verbs on the resources the server uses."""

    def __init__(self) -> None:
        self.objects: dict[str, dict[str, dict[str, Any]]] = {}
        self.fail_create: set[str] = set()

    def _bucket(self, res: Resource) -> dict[str, dict[str, Any]]:
        return self.objects.setdefault(res.path.rsplit("/", 1)[-1], {})

    async def get(self, res, name):
        return self._bucket(res).get(name)

    async def list(self, res, label_selector):
        key, _, value = label_selector.partition("=")
        return [
            o
            for o in self._bucket(res).values()
            if o["metadata"].get("labels", {}).get(key) == value
        ]

    async def create(self, res, obj):
        kind = res.path.rsplit("/", 1)[-1]
        if kind in self.fail_create:
            raise KubeError(500, "injected")
        obj = json.loads(json.dumps(obj))
        obj["metadata"]["uid"] = str(uuid.uuid4())
        obj["metadata"]["creationTimestamp"] = datetime.now(UTC).isoformat()
        self._bucket(res)[obj["metadata"]["name"]] = obj
        return obj

    async def patch(self, res, name, patch):
        obj = self._bucket(res)[name]
        for k, v in patch.get("spec", {}).items():
            obj["spec"][k] = v
        return obj

    async def delete(self, res, name):
        bucket = self._bucket(res)
        if name not in bucket:
            return False
        del bucket[name]
        # What the garbage collector does with dependents.
        if res.path.endswith("/sandboxes"):
            self.objects.get("secrets", {}).pop(f"{name}-config", None)
            self.objects.get("pods", {}).pop(name, None)
        return True

    async def close(self):
        pass

    # Test helpers ------------------------------------------------------------

    def sandbox(self, name: str) -> dict[str, Any] | None:
        return self.objects.get("sandboxes", {}).get(name)

    def set_ready(self, name: str, ready: bool = True) -> None:
        self.sandbox(name)["status"] = {
            "conditions": [{"type": "Ready", "status": "True" if ready else "False"}]
        }


@pytest.fixture
def settings(tmp_path) -> Settings:
    specs = tmp_path / "specs"
    specs.mkdir()
    (specs / "repo.json").write_text(json.dumps(SPEC))
    return Settings(
        namespace="openhands",
        data_dir=tmp_path,
        specs_dir=specs,
        default_spec="repo",
        public_url="https://openhands.example",
        internal_url="http://app-server:8080",
        webhook_url="http://app-server:8081",
        service_key=SERVICE_KEY,
        user_header=USER_HEADER,
    )


@pytest.fixture
def kube() -> FakeKube:
    return FakeKube()


@pytest.fixture
def state(settings, kube) -> State:
    s = State(settings, kube, Database(settings.data_dir / "app.db"))
    yield s
    s.db.close()


@pytest.fixture
def api(state) -> TestClient:
    return TestClient(build_api(state))


@pytest.fixture
def hooks(state) -> TestClient:
    return TestClient(build_webhooks(state))


@pytest.fixture
def user() -> dict[str, str]:
    return {USER_HEADER: "alice"}


@pytest.fixture
def service_auth(api) -> dict[str, str]:
    r = api.post(
        "/api/service/users/u/orgs/o/api-keys",
        headers={"X-Service-API-Key": SERVICE_KEY},
        json={"name": "automation"},
    )
    assert r.status_code == 200, r.text
    return {"Authorization": f"Bearer {r.json()['key']}"}
