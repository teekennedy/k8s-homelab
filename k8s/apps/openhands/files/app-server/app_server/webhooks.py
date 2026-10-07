"""Webhook receiver for sandboxes, on its own port.

Each sandbox's agent server is configured with
`{webhook_url}/sandboxes/<sandbox_id>` as a webhook base and posts
`/events/<conversation_id hex>` (a JSON list of events) and `/conversations`
(a ConversationInfo), each carrying that sandbox's own X-Session-API-Key.
Wire format: openhands-agent-server `conversation_service.py`,
WebhookSubscriber and ConversationWebhookSubscriber.

It is also the only port a sandbox can reach, so it is where an automation
run's entry point (`automation_run.py`) fetches itself, asks for its
conversation and reports the outcome.
"""

import logging
import uuid
from typing import Any

from fastapi import APIRouter, Body, HTTPException, Request, Response

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
    try:
        uuid.UUID(conversation_id)
    except ValueError:
        raise HTTPException(422, "conversation id must be a UUID")
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


@router.get("/automation/run.tar.gz")
async def automation_tarball(request: Request) -> Response:
    """What every automation defined here runs. Nothing in it is secret."""
    return Response(
        request.app.state.automations.tarball, media_type="application/gzip"
    )


@router.post("/sandboxes/{sandbox_id}/automation/conversations")
async def automation_conversation(
    sandbox_id: str, request: Request, body: dict[str, Any] = Body()
) -> dict[str, str]:
    """Start a run's conversation in the sandbox the automation service made
    for it."""
    _authenticate(request, sandbox_id)
    name = body.get("automation")
    prompt = request.app.state.automations.prompt(
        name, body.get("event"), body.get("follow_up_turns")
    )
    if prompt is None:
        raise HTTPException(404, f"no automation named {name!r} is defined")
    started = await request.app.state.conversations.start_in(
        sandbox_id,
        {
            "title": name,
            "initial_message": {
                "role": "user",
                "content": [{"type": "text", "text": prompt}],
            },
            "conversation_id": body.get("conversation_id"),
            "trigger": "automation",
        },
        "automation",
    )
    return {"id": started}


@router.post("/sandboxes/{sandbox_id}/automation/runs/{run_id}/complete")
async def automation_complete(
    sandbox_id: str, run_id: uuid.UUID, request: Request, body: dict[str, Any] = Body()
) -> Response:
    """Relay a run's completion callback (`automation/router.py`,
    complete_run): the automation service's own URL for it is the public one,
    behind a login a sandbox does not have."""
    _authenticate(request, sandbox_id)
    state = request.app.state
    resp = await state.http.post(
        f"{state.settings.automation_url}/api/automation/v1/runs/{run_id}/complete",
        json=body,
        headers={"X-Session-API-Key": state.settings.automation_api_key},
        timeout=60,
    )
    return Response(
        resp.content,
        status_code=resp.status_code,
        media_type=resp.headers.get("content-type"),
    )
