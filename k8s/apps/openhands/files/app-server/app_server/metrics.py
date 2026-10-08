"""Prometheus metrics, on a port of their own: what conversations have spent,
and how many sandboxes exist.

Usage figures are what each sandbox's agent server last reported for its
conversation (`stats.usage_to_metrics`), kept with the conversation so they
outlive the sandbox. Every series is a GAUGE, including the ones that read
like totals: they are recomputed from the conversations that still exist, so
deleting one makes the number go down, which a counter may not do.
"""

import json
from collections import defaultdict
from typing import Any

from fastapi import APIRouter, Request
from fastapi.responses import PlainTextResponse

router = APIRouter()

TOKEN_KINDS = (
    "prompt_tokens",
    "completion_tokens",
    "cache_read_tokens",
    "cache_write_tokens",
    "reasoning_tokens",
)
TITLE_LENGTH = 60


def _escape(value: Any) -> str:
    text = str(value if value is not None else "")
    return text.replace("\\", "\\\\").replace('"', '\\"').replace("\n", " ")


def _labels(**labels: Any) -> str:
    return "{" + ",".join(f'{k}="{_escape(v)}"' for k, v in labels.items()) + "}"


def render(
    conversations: list[dict[str, Any]], sandboxes: dict[tuple[str, str], int]
) -> str:
    by_status: dict[str, int] = defaultdict(int)
    cost: dict[str, float] = defaultdict(float)
    tokens: dict[tuple[str, str], int] = defaultdict(int)
    per_cost: list[str] = []
    per_tokens: list[str] = []
    for row in conversations:
        meta = json.loads(row["meta"])
        by_status[str(meta.get("execution_status") or "unknown")] += 1
        own = {
            "conversation": row["id"],
            "title": (row["title"] or "")[:TITLE_LENGTH],
            "trigger": meta.get("trigger") or "unknown",
        }
        usage = (meta.get("stats") or {}).get("usage_to_metrics") or {}
        for entry in usage.values():
            model = str(entry.get("model_name") or "unknown")
            spent = float(entry.get("accumulated_cost") or 0.0)
            cost[model] += spent
            per_cost.append(
                f"openhands_conversation_cost_usd{_labels(**own, model=model)}"
                f" {spent:.6f}"
            )
            accumulated = entry.get("accumulated_token_usage") or {}
            for kind in TOKEN_KINDS:
                count = int(accumulated.get(kind) or 0)
                tokens[(model, kind)] += count
                label = _labels(**own, model=model, kind=kind.removesuffix("_tokens"))
                per_tokens.append(f"openhands_conversation_tokens{label} {count}")

    lines = [
        "# HELP openhands_conversations Conversations held, by last reported status.",
        "# TYPE openhands_conversations gauge",
        *(
            f"openhands_conversations{_labels(status=s)} {n}"
            for s, n in sorted(by_status.items())
        ),
        "# HELP openhands_conversations_total Conversations across every status.",
        "# TYPE openhands_conversations_total gauge",
        f"openhands_conversations_total {len(conversations)}",
        "# HELP openhands_usage_cost_usd Model spend over the conversations still"
        " held, by model.",
        "# TYPE openhands_usage_cost_usd gauge",
        *(
            f"openhands_usage_cost_usd{_labels(model=m)} {v:.6f}"
            for m, v in sorted(cost.items())
        ),
        "# HELP openhands_usage_tokens Tokens over the conversations still held,"
        " by model and kind.",
        "# TYPE openhands_usage_tokens gauge",
        *(
            f"openhands_usage_tokens"
            f"{_labels(model=m, kind=k.removesuffix('_tokens'))} {v}"
            for (m, k), v in sorted(tokens.items())
        ),
        "# HELP openhands_conversation_cost_usd Model spend of one conversation.",
        "# TYPE openhands_conversation_cost_usd gauge",
        *per_cost,
        "# HELP openhands_conversation_tokens Tokens of one conversation, by kind.",
        "# TYPE openhands_conversation_tokens gauge",
        *per_tokens,
        "# HELP openhands_sandboxes Sandboxes, by spec and status.",
        "# TYPE openhands_sandboxes gauge",
        *(
            f"openhands_sandboxes{_labels(spec=spec, status=status)} {n}"
            for (spec, status), n in sorted(sandboxes.items())
        ),
    ]
    return "\n".join(lines) + "\n"


@router.get("/metrics")
async def metrics(request: Request) -> PlainTextResponse:
    state = request.app.state
    conversations = state.db.all(
        "SELECT id, title, meta FROM conversations WHERE deleted_at IS NULL"
    )
    statuses = await state.sandboxes.statuses()
    sandboxes: dict[tuple[str, str], int] = defaultdict(int)
    for row in state.db.all("SELECT id, spec FROM sandboxes WHERE deleted_at IS NULL"):
        sandboxes[(row["spec"], statuses.get(row["id"], "MISSING"))] += 1
    return PlainTextResponse(
        render(conversations, sandboxes),
        media_type="text/plain; version=0.0.4; charset=utf-8",
    )


@router.get("/healthz", include_in_schema=False)
async def healthz() -> dict[str, bool]:
    return {"ok": True}
