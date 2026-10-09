"""Terminal-tab events for an ACP agent's shell commands.

The canvas fills its terminal tab only from `TerminalAction` actions and
`TerminalObservation` observations. An ACP agent runs its commands inside the
adapter and reports each as an `ACPToolCallEvent`, which the frontend draws as
a chat card and never offers to the tab. `TerminalMirror` turns an execute-kind
tool call into the pair the tab reads, leaving the original event untouched.
"""

import json
import uuid
from typing import Any

_FINAL = {"completed", "failed"}
_NAMESPACE = uuid.UUID("6f1c1a0e-5b36-4b0a-9a53-1d3f6f0f6c11")


def _id(*parts: str) -> str:
    return str(uuid.uuid5(_NAMESPACE, "/".join(parts)))


def _text(raw_output: Any, content: Any) -> str:
    """The command's output, from `raw_output` or else the text blocks of
    `content` (the adapter fences those in a console code block)."""
    if isinstance(raw_output, str) and raw_output:
        return raw_output
    blocks = []
    for item in content or []:
        inner = item.get("content") if isinstance(item, dict) else None
        if isinstance(inner, dict) and isinstance(inner.get("text"), str):
            blocks.append(inner["text"])
    return "\n".join(blocks)


class TerminalMirror:
    """Per-stream state: which tool calls already have their action."""

    def __init__(self) -> None:
        self._started: set[str] = set()

    def mirror(self, event: dict[str, Any]) -> list[dict[str, Any]]:
        if event.get("kind") != "ACPToolCallEvent" or event.get("tool_kind") != (
            "execute"
        ):
            return []
        call_id = event.get("tool_call_id")
        command = (event.get("raw_input") or {}).get("command")
        if not call_id or not isinstance(command, str):
            return []
        out: list[dict[str, Any]] = []
        action_id = _id(call_id, "action")
        if call_id not in self._started:
            self._started.add(call_id)
            out.append(
                {
                    "kind": "ActionEvent",
                    "id": action_id,
                    "timestamp": event.get("timestamp"),
                    "source": "agent",
                    "tool_name": "terminal",
                    "tool_call_id": call_id,
                    "action": {"kind": "TerminalAction", "command": command},
                }
            )
        if event.get("status") in _FINAL:
            self._started.discard(call_id)
            # The adapter reports no exit code; the status is all it knows.
            exit_code = 1 if event.get("status") == "failed" else 0
            out.append(
                {
                    "kind": "ObservationEvent",
                    "id": _id(call_id, "observation"),
                    "timestamp": event.get("timestamp"),
                    "source": "environment",
                    "tool_name": "terminal",
                    "tool_call_id": call_id,
                    "action_id": action_id,
                    "observation": {
                        "kind": "TerminalObservation",
                        "command": command,
                        "exit_code": exit_code,
                        "timeout": False,
                        "metadata": {
                            "exit_code": exit_code,
                            "pid": -1,
                            "working_dir": None,
                            "prefix": "",
                            "suffix": "",
                        },
                        "content": [
                            {
                                "type": "text",
                                "text": _text(
                                    event.get("raw_output"), event.get("content")
                                ),
                            }
                        ],
                        "is_error": bool(event.get("is_error")),
                    },
                }
            )
        return out

    def frames(self, message: str) -> list[str]:
        """The frames to send in place of one upstream text frame: the frame
        itself, or for a shell command the terminal events that stand in for
        it, since the canvas would otherwise draw a card for each."""
        try:
            event = json.loads(message)
        except ValueError:
            return [message]
        if not isinstance(event, dict):
            return [message]
        mirrored = self.mirror(event)
        if not mirrored:
            return [message]
        return [json.dumps(e) for e in mirrored]
