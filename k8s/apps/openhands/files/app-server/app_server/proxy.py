"""/runtime/<sandbox_id>/** — reverse proxy to a sandbox's agent server.

The agent server has no notion of a path prefix, so the prefix is stripped.
The frontend derives both the HTTP base and the WebSocket URL from the
conversation_url it is given, so one route serves both.

POST /api/cloud-proxy is the same thing in an envelope: the frontend's cloud
client sends every runtime call it has no app-API endpoint for through it
(typescript client, `CloudClient.requestThroughProxy`).
"""

import asyncio
import contextlib
import logging
from typing import Any

import httpx
import websockets
from fastapi import (
    APIRouter,
    Depends,
    HTTPException,
    Request,
    Response,
    WebSocket,
    WebSocketDisconnect,
)
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field
from starlette.background import BackgroundTask

from .auth import principal
from .sandboxes import SandboxManager
from .terminal_events import TerminalMirror

log = logging.getLogger(__name__)
router = APIRouter()

HOP_BY_HOP = {
    "connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    "host",
    # Recomputed for the body actually sent upstream.
    "content-length",
}
# Credentials for *this* origin. An agent server is code the agent controls, so
# nothing that authenticates anywhere else may reach it.
CREDENTIALS = {"cookie", "authorization"}
PROXY_METHODS = {"GET", "POST", "PUT", "PATCH", "DELETE"}


def upstream_headers(headers, user_header: str) -> dict[str, str]:
    drop = HOP_BY_HOP | CREDENTIALS | {user_header.lower()}
    return {
        k: v
        for k, v in headers.items()
        if k.lower() not in drop
        and not k.lower().startswith("x-forwarded-")
        and not k.lower().startswith("sec-websocket-")
    }


def _authorized(
    sandboxes: SandboxManager, sandbox_id: str, headers, query, user_header: str
) -> bool:
    """A browser carries the oauth2-proxy identity; the automation service
    carries the sandbox's own session key (the agent server checks it too)."""
    if headers.get(user_header):
        return True
    given = headers.get("X-Session-API-Key") or query.get("session_api_key")
    return sandboxes.check_session_key(sandbox_id, given)


@router.api_route(
    "/runtime/{sandbox_id}/{path:path}",
    methods=[*sorted(PROXY_METHODS), "HEAD", "OPTIONS"],
    include_in_schema=False,
)
async def proxy_http(sandbox_id: str, path: str, request: Request):
    state = request.app.state
    sandboxes: SandboxManager = state.sandboxes
    if sandboxes.live_row(sandbox_id) is None:
        raise HTTPException(404, "unknown sandbox")
    user_header = state.settings.user_header
    if not _authorized(
        sandboxes, sandbox_id, request.headers, request.query_params, user_header
    ):
        raise HTTPException(401, "not authenticated")
    sandboxes.touch(sandbox_id)

    return await forward(
        request, f"{sandboxes.agent_url(sandbox_id)}/{path}", "sandbox"
    )


async def forward(
    request: Request, url: str, what: str, extra_headers: dict[str, str] | None = None
) -> StreamingResponse:
    """Relay one HTTP request upstream and stream the response back, with
    this origin's credentials stripped (see upstream_headers)."""
    client: httpx.AsyncClient = request.app.state.http
    headers = upstream_headers(request.headers, request.app.state.settings.user_header)
    upstream = client.build_request(
        request.method,
        url,
        params=request.query_params,
        headers={**headers, **(extra_headers or {})},
        content=await request.body(),
    )
    try:
        resp = await client.send(upstream, stream=True)
    except httpx.ConnectError:
        raise HTTPException(502, f"{what} is not reachable")
    return StreamingResponse(
        resp.aiter_raw(),
        status_code=resp.status_code,
        # aiter_raw passes the body through still encoded, so content-length
        # and content-encoding stay true.
        headers={
            k: v
            for k, v in resp.headers.items()
            if k.lower() not in HOP_BY_HOP - {"content-length"}
        },
        background=BackgroundTask(resp.aclose),
    )


class CloudProxyRequest(BaseModel):
    """The agent server's own envelope (`cloud_proxy_router.py`)."""

    host: str
    method: str = "GET"
    path: str
    headers: dict[str, str] = Field(default_factory=dict)
    body: Any = None
    timeout_seconds: float = Field(default=15.0, ge=1.0, le=60.0)


def _sandbox_in(host: str, public_url: str) -> str | None:
    """The sandbox id in a `{public_url}/runtime/<id>` base URL, the form the
    frontend is handed in conversation_url; None for any other host."""
    prefix = f"{public_url}/runtime/"
    if not host.startswith(prefix):
        return None
    return host[len(prefix) :].rstrip("/")


@router.post(
    "/api/cloud-proxy", include_in_schema=False, dependencies=[Depends(principal)]
)
async def cloud_proxy(req: CloudProxyRequest, request: Request) -> Response:
    state = request.app.state
    sandboxes: SandboxManager = state.sandboxes
    # The only host this relays to is a sandbox this server minted. Anything
    # else would make it an open relay into the cluster.
    sandbox_id = _sandbox_in(req.host, state.settings.public_url)
    if sandbox_id is None or sandboxes.live_row(sandbox_id) is None:
        raise HTTPException(403, "cloud proxy host not allowed")
    method = req.method.upper()
    if method not in PROXY_METHODS or not req.path.startswith("/"):
        raise HTTPException(422, "invalid method or path")
    sandboxes.touch(sandbox_id)

    # Of the envelope's headers only the session key means anything to an
    # agent server; the rest were addressed to a cloud host.
    headers = {k: v for k, v in req.headers.items() if k.lower() == "x-session-api-key"}
    body: dict[str, Any] = {}
    if isinstance(req.body, str):
        body["content"] = req.body
    elif req.body is not None:
        body["json"] = req.body
    try:
        resp = await state.http.request(
            method,
            f"{sandboxes.agent_url(sandbox_id)}{req.path}",
            headers=headers,
            timeout=req.timeout_seconds,
            **body,
        )
    except httpx.RequestError:
        raise HTTPException(502, "sandbox is not reachable")
    return Response(
        resp.content,
        status_code=resp.status_code,
        media_type=resp.headers.get("content-type", "application/octet-stream"),
    )


@router.websocket("/runtime/{sandbox_id}/{path:path}")
async def proxy_ws(ws: WebSocket, sandbox_id: str, path: str):
    state = ws.app.state
    sandboxes: SandboxManager = state.sandboxes
    user_header = state.settings.user_header
    if sandboxes.live_row(sandbox_id) is None or not _authorized(
        sandboxes, sandbox_id, ws.headers, ws.query_params, user_header
    ):
        await ws.close(code=4401)
        return
    sandboxes.touch(sandbox_id)

    base = sandboxes.agent_url(sandbox_id).replace("http://", "ws://", 1)
    url = f"{base}/{path}" + (f"?{ws.url.query}" if ws.url.query else "")
    try:
        upstream = await websockets.connect(
            url,
            additional_headers=upstream_headers(ws.headers, user_header),
            max_size=None,
            open_timeout=15,
        )
    except Exception as e:
        log.warning("ws connect to %s failed: %s", sandbox_id, e)
        await ws.close(code=1011)
        return
    await ws.accept()

    async def client_to_upstream():
        while True:
            msg = await ws.receive()
            if msg["type"] == "websocket.disconnect":
                return
            if msg.get("text") is not None:
                await upstream.send(msg["text"])
            elif msg.get("bytes") is not None:
                await upstream.send(msg["bytes"])

    mirror = TerminalMirror()

    async def upstream_to_client():
        async for msg in upstream:
            # A stream that is carrying events is a sandbox in use.
            sandboxes.touch(sandbox_id)
            if isinstance(msg, bytes):
                await ws.send_bytes(msg)
            else:
                for frame in mirror.frames(msg):
                    await ws.send_text(frame)

    tasks = [
        asyncio.create_task(client_to_upstream()),
        asyncio.create_task(upstream_to_client()),
    ]
    try:
        await asyncio.wait(tasks, return_when=asyncio.FIRST_COMPLETED)
    finally:
        for t in tasks:
            t.cancel()
        await upstream.close()
        # The client may already be gone; there is nothing left to tell it.
        with contextlib.suppress(RuntimeError, WebSocketDisconnect):
            await ws.close()
