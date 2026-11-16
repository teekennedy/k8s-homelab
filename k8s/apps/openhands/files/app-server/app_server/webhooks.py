"""Webhook receiver for sandboxes, on its own port.

Each sandbox's agent server is configured with
`{webhook_url}/sandboxes/<sandbox_id>` as a webhook base and posts
`/events/<conversation_id hex>` (a JSON list of events) and `/conversations`
(a ConversationInfo), each carrying that sandbox's own X-Session-API-Key.
Wire format: openhands-agent-server `conversation_service.py`,
WebhookSubscriber and ConversationWebhookSubscriber.

It is also the only port a sandbox can reach, so it is where an automation
run's entry point (`automation_run.py`) asks for its conversation and reports
the outcome.
"""

import logging
import uuid
from typing import Any

from fastapi import APIRouter, Body, HTTPException, Request, Response
from starlette.background import BackgroundTask

from . import alerts, notify
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


@router.post("/sandboxes/{sandbox_id}/automation/conversations")
async def automation_conversation(
    sandbox_id: str, request: Request, body: dict[str, Any] = Body()
) -> dict[str, str]:
    """Start a run's conversation in the sandbox the automation service made
    for it."""
    _authenticate(request, sandbox_id)
    automations = request.app.state.automations
    name = body.get("automation")
    definition = automations.definition(name)
    if definition is None:
        raise HTTPException(404, f"no automation named {name!r} is defined")
    prompt = automations.prompt(
        definition, body.get("event"), body.get("follow_up_turns")
    )
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
            "selected_repository": definition.get("repository"),
            "selected_branch": definition.get("branch"),
            "acp_model": definition.get("model"),
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
    behind a login a sandbox does not have. The service deletes the sandbox
    on receiving it, so the transcript is completed first."""
    _authenticate(request, sandbox_id)
    state = request.app.state
    # Ours, not the service's: its schema rejects unknown fields. Carried by
    # the run script rather than recorded when the run asked for its
    # conversation, so a run that never got that far still reports.
    name = body.pop("automation", None)
    if body.get("conversation_id"):
        try:
            await state.conversations.drain(sandbox_id, body["conversation_id"])
        except Exception:
            log.exception("could not read the last events of %s", sandbox_id)
    resp = await state.http.post(
        f"{state.settings.automation_url}/api/automation/v1/runs/{run_id}/complete",
        json=body,
        headers={"X-Session-API-Key": state.settings.automation_api_key},
        timeout=60,
    )
    definition = state.automations.definition(name) or {}
    if (
        definition.get("trigger", {}).get("source") == alerts.SOURCE
        and body.get("status") == "COMPLETED"
    ):
        state.alerts.triaged()
    return Response(
        resp.content,
        status_code=resp.status_code,
        media_type=resp.headers.get("content-type"),
        # After the response: the run script is waiting on it, not on Discord.
        background=BackgroundTask(_report, state, name, definition, body),
    )


async def _report(
    state: Any, name: str | None, definition: dict[str, Any], body: dict[str, Any]
) -> None:
    """Post a finished run's outcome where its automation's `notify` says."""
    if definition.get("notify") != "discord":
        return
    try:
        await _post_report(state, name, body)
    except Exception:
        log.exception("could not report the outcome of %s", name)


async def _post_report(state: Any, name: str, body: dict[str, Any]) -> None:
    conv_id = body.get("conversation_id")
    ok = body.get("status") == "COMPLETED"
    text = state.conversations.final_message(conv_id) if ok and conv_id else None
    if not text:
        text = f"The run did not finish: {body.get('error') or body.get('status')}"
        ok = False
    await notify.discord(
        state.http,
        state.settings.discord_webhook_file,
        avatar_url=state.settings.discord_avatar_url,
        title=name,
        url=(
            f"{state.settings.public_url}/conversations/{conv_id}"
            if conv_id
            else state.settings.public_url
        ),
        text=text,
        ok=ok,
    )
