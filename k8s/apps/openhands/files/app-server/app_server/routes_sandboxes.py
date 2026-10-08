"""/api/v1/sandboxes and the service key endpoint.

Shapes follow what their two callers read: the canvas frontend
(`api/cloud/sandbox-service.types.d.ts`, V1SandboxInfo) and the automation
service's CloudSandboxBackend (`openhands/automation/backends/cloud.py`).
"""

from typing import Annotated, Any

from fastapi import APIRouter, Body, Depends, HTTPException, Query, Request
from fastapi.responses import JSONResponse

from .auth import Principal, check_service_key, mint_api_key, principal
from .sandboxes import AtCapacity, SandboxManager, UnknownSpec

router = APIRouter()
Caller = Annotated[Principal, Depends(principal)]


def _base_url(request: Request, who: Principal) -> str:
    """Where the caller reaches /runtime: the browser through the canvas
    origin, the automation service directly over the cluster network."""
    s = request.app.state.settings
    return s.public_url if who.kind == "user" else s.internal_url


def _sandboxes(request: Request) -> SandboxManager:
    return request.app.state.sandboxes


@router.get("/api/v1/sandboxes")
async def batch_get(
    request: Request, who: Caller, id: Annotated[list[str], Query()] = []
) -> list[dict[str, Any] | None]:
    sandboxes = _sandboxes(request)
    base = _base_url(request, who)
    statuses = await sandboxes.statuses()
    rows = [sandboxes.row(sandbox_id) for sandbox_id in id]
    return [
        sandboxes.info(r, statuses.get(r["id"], "MISSING"), base) if r else None
        for r in rows
    ]


@router.get("/api/v1/sandboxes/search")
async def search(
    request: Request,
    who: Caller,
    limit: Annotated[int, Query(ge=1, le=100)] = 100,
    page_id: str | None = None,
) -> dict[str, Any]:
    sandboxes = _sandboxes(request)
    base = _base_url(request, who)
    rows, next_page = sandboxes.page(limit, page_id)
    statuses = await sandboxes.statuses()
    return {
        "items": [
            sandboxes.info(r, statuses.get(r["id"], "MISSING"), base) for r in rows
        ],
        "next_page_id": next_page,
    }


@router.post("/api/v1/sandboxes")
async def create(
    request: Request,
    who: Caller,
    body: Annotated[dict[str, Any] | None, Body()] = None,
):
    sandboxes = _sandboxes(request)
    spec = (body or {}).get(
        "sandbox_spec_id"
    ) or request.app.state.settings.default_spec
    try:
        row = await sandboxes.create(spec, who.name, who.kind)
    except UnknownSpec:
        raise HTTPException(400, f"unknown sandbox spec {spec!r}")
    except AtCapacity as e:
        # What the automation service reads to mark a run skipped, not failed
        # (`backends/cloud.py`).
        return JSONResponse({"message": str(e), "detail": str(e)}, status_code=429)
    return sandboxes.info(row, "STARTING", _base_url(request, who))


@router.post("/api/v1/sandboxes/{sandbox_id}/pause")
async def pause(sandbox_id: str, request: Request, who: Caller) -> dict[str, bool]:
    if not await _sandboxes(request).pause(sandbox_id):
        raise HTTPException(404, "unknown sandbox")
    return {"success": True}


@router.post("/api/v1/sandboxes/{sandbox_id}/resume")
async def resume(sandbox_id: str, request: Request, who: Caller) -> dict[str, bool]:
    if not await _sandboxes(request).resume(sandbox_id):
        raise HTTPException(404, "unknown sandbox")
    return {"success": True}


@router.delete("/api/v1/sandboxes/{sandbox_id}")
async def delete(sandbox_id: str, request: Request, who: Caller) -> dict[str, bool]:
    if not await _sandboxes(request).delete(sandbox_id):
        raise HTTPException(404, "unknown sandbox")
    return {"success": True}


@router.post("/api/service/users/{user_id}/orgs/{org_id}/api-keys")
async def service_mint_key(
    user_id: str,
    org_id: str,
    request: Request,
    body: Annotated[dict[str, Any] | None, Body()] = None,
) -> dict[str, str]:
    check_service_key(request, request.app.state.settings.service_key)
    name = (body or {}).get("name") or "service"
    key = mint_api_key(request.app.state.db, name)
    return {"key": key, "name": name}
