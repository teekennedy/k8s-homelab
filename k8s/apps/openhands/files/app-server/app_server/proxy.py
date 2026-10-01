"""/runtime/<sandbox_id>/** — reverse proxy to a sandbox's agent server.

The agent server has no notion of a path prefix, so the prefix is stripped.
The frontend derives both the HTTP base and the WebSocket URL from the
conversation_url it is given, so one route serves both.
"""

import asyncio
import logging

import httpx
import websockets
from fastapi import APIRouter, HTTPException, Request, WebSocket
from fastapi.responses import StreamingResponse
from starlette.background import BackgroundTask

from .sandboxes import SandboxManager

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
    methods=["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"],
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

    client: httpx.AsyncClient = state.http
    upstream = client.build_request(
        request.method,
        f"{sandboxes.agent_url(sandbox_id)}/{path}",
        params=request.query_params,
        headers=upstream_headers(request.headers, user_header),
        content=await request.body(),
    )
    try:
        resp = await client.send(upstream, stream=True)
    except httpx.ConnectError:
        raise HTTPException(502, "sandbox is not reachable")
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

    async def upstream_to_client():
        async for msg in upstream:
            if isinstance(msg, bytes):
                await ws.send_bytes(msg)
            else:
                await ws.send_text(msg)

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
        try:
            await ws.close()
        except RuntimeError:
            pass  # already closed by the client
