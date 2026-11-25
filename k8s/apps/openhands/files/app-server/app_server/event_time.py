"""UTC markers on event timestamps sent to the browser.

The agent server stamps events with a naive UTC isoformat, such as
`2026-11-22T10:11:50.649600`. JavaScript's `Date` reads a timestamp with no
offset as local time, so the canvas shows and exports every event shifted by
the browser's UTC offset. Events are stored as the agent server sent them, and
marked as UTC only on the way out.
"""

import json
from datetime import datetime
from typing import Any


def mark_utc(event: dict[str, Any]) -> dict[str, Any]:
    """The event with a `Z` on its timestamp, if that timestamp is naive."""
    ts = event.get("timestamp")
    if not isinstance(ts, str):
        return event
    try:
        parsed = datetime.fromisoformat(ts)
    except ValueError:
        return event
    if parsed.tzinfo is not None:
        return event
    return event | {"timestamp": ts + "Z"}


def mark_utc_frame(message: str) -> str:
    """A WebSocket text frame with its event's timestamp marked as UTC. Frames
    that are not a JSON event, or need no change, pass through as they are."""
    try:
        event = json.loads(message)
    except ValueError:
        return message
    if not isinstance(event, dict):
        return message
    marked = mark_utc(event)
    return message if marked is event else json.dumps(marked)
