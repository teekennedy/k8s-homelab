"""Webhook receiver for sandboxes, on its own port.

Each sandbox's agent server is configured with
`{webhook_url}/sandboxes/<sandbox_id>` as a webhook base and posts
`/events/<conversation_id hex>` (a JSON list of events) and `/conversations`
(a ConversationInfo), each carrying that sandbox's own X-Session-API-Key.
Wire format: openhands-agent-server `conversation_service.py`,
WebhookSubscriber and ConversationWebhookSubscriber.
"""

import logging
from typing import Any

from fastapi import APIRouter, Body, HTTPException, Request

from .sandboxes import SandboxManager

log = logging.getLogger(__name__)
router = APIRouter()


def _authenticate(request: Request, sandbox_id: str) -> None:
    sandboxes: SandboxManager = request.app.state.sandboxes
    if not sandboxes.check_session_key(
        sandbox_id, request.headers.get("X-Session-API-Key")
    ):
        raise HTTPException(401, "not authenticated")


@router.post("/sandboxes/{sandbox_id}/events/{conversation_id}")
async def events(
    sandbox_id: str,
    conversation_id: str,
    request: Request,
    body: list[dict[str, Any]] = Body(),
) -> dict[str, bool]:
    _authenticate(request, sandbox_id)
    sink = request.app.state.event_sink
    if sink is not None:
        await sink.events(sandbox_id, conversation_id, body)
    return {"success": True}


@router.post("/sandboxes/{sandbox_id}/conversations")
async def conversations(
    sandbox_id: str, request: Request, body: dict[str, Any] = Body()
) -> dict[str, bool]:
    _authenticate(request, sandbox_id)
    sink = request.app.state.event_sink
    if sink is not None:
        await sink.conversation(sandbox_id, body)
    return {"success": True}
