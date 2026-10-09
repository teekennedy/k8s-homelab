"""A subscription's usage limit, as it reaches a conversation.

The agent server ends the turn with a ConversationErrorEvent whose detail is
the provider's own error body. ChatGPT's says when the window resets:

    {"error": {"type": "usage_limit_reached", "resets_at": 1791533677,
               "limit_window_minutes": 300, "resets_in_seconds": 10727}}

An ordinary rate limit carries no reset time and is left to the SDK's retries.
"""

import re
import uuid
from datetime import UTC, datetime, timedelta
from typing import Any

_RESETS_AT = re.compile(r'"resets_at"\s*:\s*(\d+)')
_RESETS_IN = re.compile(r'"resets_in_seconds"\s*:\s*(\d+)')


def event_time(event: dict[str, Any]) -> datetime:
    """Event timestamps are naive UTC."""
    return datetime.fromisoformat(event["timestamp"]).replace(tzinfo=UTC)


def reset_time(event: dict[str, Any]) -> datetime | None:
    """When the limit this event reports is lifted; None for any other event."""
    if event.get("kind") != "ConversationErrorEvent":
        return None
    detail = event.get("detail") or ""
    if "usage_limit_reached" not in detail:
        return None
    if match := _RESETS_AT.search(detail):
        return datetime.fromtimestamp(int(match[1]), UTC)
    if match := _RESETS_IN.search(detail):
        return event_time(event) + timedelta(seconds=int(match[1]))
    return None


def _duration(seconds: float) -> str:
    minutes = max(1, round(seconds / 60))
    hours, minutes = divmod(minutes, 60)
    if not hours:
        return f"{minutes} min"
    return f"{hours} h {minutes} min" if minutes else f"{hours} h"


def notice_text(reset: datetime, at: datetime, outcome: str) -> str:
    """What the transcript says. `outcome` is what was done about the limit:
    `waiting` (continues by itself), `suspended`, or `ended`."""
    when = f"{reset:%H:%M} UTC on {reset.day} {reset:%b}"
    left = (reset - at).total_seconds()
    resets = (
        f"It resets at {when}, in {_duration(left)}."
        if left > 0
        else f"It reset at {when}."
    )
    text = f"**Usage limit reached.** {resets}"
    if outcome == "waiting":
        return f"{text} This conversation will continue by itself then."
    if outcome == "suspended":
        return (
            f"{text} That is too long to wait, so the sandbox has been suspended."
            " Send a message after the reset to continue here, or start a new"
            " conversation."
        )
    return f"{text} The agent stopped where the transcript ends."


def notice_event(error: dict[str, Any], text: str) -> dict[str, Any]:
    """An agent message for the history kept here, placed right after the
    error it explains: a stored transcript otherwise just stops. Its id is
    derived from the error's, so a redelivered error adds nothing."""
    at = datetime.fromisoformat(error["timestamp"]) + timedelta(milliseconds=1)
    return {
        "id": str(uuid.uuid5(uuid.NAMESPACE_URL, f"usage-limit:{error['id']}")),
        "timestamp": at.isoformat(),
        "source": "agent",
        "parent_id": error["id"],
        "llm_message": {
            "role": "assistant",
            "content": [{"cache_prompt": False, "type": "text", "text": text}],
            "tool_calls": None,
            "tool_call_id": None,
            "name": None,
            "reasoning_content": None,
            "thinking_blocks": [],
            "responses_reasoning_item": None,
        },
        "llm_response_id": None,
        "activated_skills": [],
        "extended_content": [],
        "sender": None,
        "critic_result": None,
        "kind": "MessageEvent",
    }
