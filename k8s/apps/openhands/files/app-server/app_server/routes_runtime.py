"""Per-conversation endpoints the frontend calls on the app API in cloud mode
and that are answered by the conversation's own agent server.

Each maps onto the agent server call the frontend makes itself in local mode
(see the typescript client's FileClient, BashClient, SkillsClient and
ConversationClient, and `hooks/query/use-workspace-files.js`). Without a
RUNNING sandbox there is nothing to ask: listings come back empty, single
items 404, and the frontend shows the conversation from its history.
"""

from typing import Annotated, Any
import json
import uuid

from fastapi import APIRouter, Body, Depends, HTTPException, Request
from fastapi.responses import JSONResponse, PlainTextResponse

from .auth import principal
from .conversations import WORKING_DIR, ConversationService
from .proxy import forward
from .routes_settings import _profiles, _store

router = APIRouter(dependencies=[Depends(principal)])
BASE = "/api/v1/app-conversations/{conv_id}"

# The frontend's own workspace listing (use-workspace-files.js): bounded, and
# skipping the directories nobody wants to browse.
FILE_LIMIT = 2000
PRUNE = (
    ".git node_modules .venv venv __pycache__ dist build .next .cache"
    " .pytest_cache .mypy_cache .turbo .parcel-cache target"
).split()
FIND = (
    "find . \\( "
    + " -o ".join(f"-name '{d}' -prune" for d in PRUNE)
    + f" \\) -o -type f -print 2>/dev/null | sort | head -n {FILE_LIMIT}"
)


def _svc(request: Request) -> ConversationService:
    return request.app.state.conversations


def _known(request: Request, conv_id: str) -> None:
    if _svc(request).row(conv_id) is None:
        raise HTTPException(404, "unknown conversation")


async def _call(
    request: Request, conv_id: str, method: str, path: str, **kw: Any
) -> Any | None:
    """JSON from the conversation's agent server, or None with no runtime."""
    runtime = await _svc(request).runtime(conv_id)
    if runtime is None:
        return None
    url, key = runtime
    resp = await request.app.state.http.request(
        method, f"{url}{path}", headers={"X-Session-API-Key": key}, timeout=60, **kw
    )
    if resp.status_code >= 400:
        raise HTTPException(resp.status_code, resp.text[:500])
    return resp.json() if resp.content else {}


@router.get(f"{BASE}/skills")
async def skills(conv_id: str, request: Request) -> dict[str, Any]:
    _known(request, conv_id)
    got = await _call(
        request,
        conv_id,
        "POST",
        "/api/skills",
        json={
            "load_public": False,
            "load_user": True,
            "load_project": True,
            "load_org": False,
            "project_dir": WORKING_DIR,
        },
    )
    return got or {"skills": []}


@router.get(f"{BASE}/files")
async def files(conv_id: str, request: Request, path: str = WORKING_DIR) -> list[str]:
    _known(request, conv_id)
    got = await _call(
        request,
        conv_id,
        "POST",
        "/api/bash/execute_bash_command",
        json={"command": FIND, "cwd": path, "timeout": 30},
    )
    if got is None:
        return []
    if got.get("exit_code") not in (0, None):
        raise HTTPException(502, got.get("stderr") or "listing files failed")
    lines = (line.strip() for line in (got.get("stdout") or "").splitlines())
    return list(dict.fromkeys(line.removeprefix("./") for line in lines if line))


@router.get(f"{BASE}/file")
async def file(conv_id: str, file_path: str, request: Request):
    _known(request, conv_id)
    runtime = await _svc(request).runtime(conv_id)
    if runtime is None:
        raise HTTPException(404, "the conversation's sandbox is not running")
    url, key = runtime
    resp = await request.app.state.http.get(
        f"{url}/api/file/download",
        params={"path": file_path},
        headers={"X-Session-API-Key": key},
    )
    if resp.status_code >= 400:
        raise HTTPException(resp.status_code, resp.text[:500])
    return PlainTextResponse(resp.text)


@router.get(f"{BASE}/download")
async def download(conv_id: str, request: Request):
    _known(request, conv_id)
    runtime = await _svc(request).runtime(conv_id)
    if runtime is None:
        raise HTTPException(404, "the conversation's sandbox is not running")
    url, key = runtime
    return await forward(
        request,
        f"{url}/api/file/download-trajectory/{conv_id}",
        "sandbox",
        {"X-Session-API-Key": key},
    )


@router.get(f"{BASE}/git/changes")
async def git_changes(
    conv_id: str, request: Request, path: str = WORKING_DIR
) -> list[Any]:
    _known(request, conv_id)
    got = await _call(
        request, conv_id, "GET", "/api/git/changes", params={"path": path}
    )
    return got if got is not None else []


@router.get(f"{BASE}/git/diff")
async def git_diff(conv_id: str, request: Request, path: str) -> JSONResponse:
    _known(request, conv_id)
    got = await _call(request, conv_id, "GET", "/api/git/diff", params={"path": path})
    if got is None:
        raise HTTPException(404, "the conversation's sandbox is not running")
    return JSONResponse(got)


@router.post(f"{BASE}/switch_acp_model")
async def switch_acp_model(
    conv_id: str, request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, bool]:
    _known(request, conv_id)
    model = body.get("model")
    if not model:
        raise HTTPException(422, "model is required")
    resp = await _call(
        request,
        conv_id,
        "POST",
        f"/api/conversations/{conv_id}/switch_acp_model",
        json={"model": model},
    )
    if resp is None:
        raise HTTPException(409, "the conversation's sandbox is not running")
    _svc(request).set_llm_model(conv_id, model)
    return {"success": True}


@router.post(f"{BASE}/switch_profile")
async def switch_profile(
    conv_id: str, request: Request, body: Annotated[dict[str, Any], Body()]
) -> dict[str, bool]:
    _known(request, conv_id)
    row = _svc(request).row(conv_id)
    if json.loads(row["meta"]).get("agent_kind") == "acp":
        raise HTTPException(409, "ACP conversations use switch_acp_model")
    name = body.get("profile_name")
    if not isinstance(name, str) or not name:
        raise HTTPException(422, "profile_name is required")
    llm = _profiles(lambda: _store(request).llm_config(name))
    llm["usage_id"] = f"profile:{name}:{uuid.uuid4()}"
    resp = await _call(
        request,
        conv_id,
        "POST",
        f"/api/conversations/{conv_id}/switch_llm",
        json={"llm": llm},
    )
    if resp is None:
        raise HTTPException(409, "the conversation's sandbox is not running")
    _svc(request).set_llm_model(conv_id, llm["model"])
    return {"success": True}
