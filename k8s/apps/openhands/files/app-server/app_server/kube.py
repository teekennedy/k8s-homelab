"""The handful of Kubernetes API calls this server makes, over plain HTTP.

The official client is synchronous and pulls in a large dependency tree for
five verbs on three resource types; this keeps the event loop unblocked.
"""

from pathlib import Path
from typing import Any

import httpx

SA_DIR = Path("/var/run/secrets/kubernetes.io/serviceaccount")


class KubeError(Exception):
    def __init__(self, status: int, body: str):
        super().__init__(f"Kubernetes API {status}: {body[:500]}")
        self.status = status


class Resource:
    """An API path prefix for one resource type in one namespace."""

    def __init__(self, group: str, version: str, plural: str, namespace: str):
        base = f"/apis/{group}/{version}" if group else f"/api/{version}"
        self.path = f"{base}/namespaces/{namespace}/{plural}"


class Kube:
    def __init__(self, client: httpx.AsyncClient):
        self._client = client

    @classmethod
    def in_cluster(cls) -> "Kube":
        token = (SA_DIR / "token").read_text().strip()
        client = httpx.AsyncClient(
            base_url="https://kubernetes.default.svc",
            verify=str(SA_DIR / "ca.crt"),
            headers={"Authorization": f"Bearer {token}"},
            timeout=30,
        )
        return cls(client)

    async def _call(self, method: str, url: str, **kw: Any) -> dict[str, Any]:
        r = await self._client.request(method, url, **kw)
        if r.status_code >= 400:
            raise KubeError(r.status_code, r.text)
        return r.json()

    async def get(self, res: Resource, name: str) -> dict[str, Any] | None:
        try:
            return await self._call("GET", f"{res.path}/{name}")
        except KubeError as e:
            if e.status == 404:
                return None
            raise

    async def list(self, res: Resource, label_selector: str) -> list[dict[str, Any]]:
        body = await self._call(
            "GET", res.path, params={"labelSelector": label_selector}
        )
        return body.get("items", [])

    async def create(self, res: Resource, obj: dict[str, Any]) -> dict[str, Any]:
        return await self._call("POST", res.path, json=obj)

    async def patch(
        self, res: Resource, name: str, patch: dict[str, Any]
    ) -> dict[str, Any]:
        return await self._call(
            "PATCH",
            f"{res.path}/{name}",
            json=patch,
            headers={"Content-Type": "application/merge-patch+json"},
        )

    async def delete(self, res: Resource, name: str) -> bool:
        """Delete, letting the garbage collector reap dependents (pod, PVC,
        config Secret); False if it was already gone."""
        try:
            await self._call(
                "DELETE",
                f"{res.path}/{name}",
                json={"propagationPolicy": "Background"},
            )
            return True
        except KubeError as e:
            if e.status == 404:
                return False
            raise

    async def close(self) -> None:
        await self._client.aclose()
