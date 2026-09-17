"""Prometheus exporter for agent session usage.

The agent server keeps per-conversation token and cost figures but exposes no
/metrics endpoint of its own, and there is no upstream usage dashboard. This
walks the conversation list on each scrape and renders what is there.

Every series is a GAUGE, including the ones whose names read like totals. They
are recomputed from the conversations that exist right now, so deleting one
makes the number go down — which a counter is not allowed to do. Use them as
"what this deployment has spent on the history it still holds", and reach for
`max_over_time` rather than `rate` when charting them.

Stdlib only, so a bare python image is the whole runtime.
"""

from __future__ import annotations

import json
import logging
import os
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from collections import defaultdict
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG_LEVEL = os.environ.get("LOG_LEVEL", "info").upper()
PORT = int(os.environ.get("METRICS_PORT", "9102"))
AGENT_SERVER_URL = os.environ.get("AGENT_SERVER_URL", "http://127.0.0.1:18000")
# Written by the image's entrypoint on first boot; the sidecar shares the data
# volume rather than duplicating the key into a Secret that would then have two
# owners.
API_KEY_FILE = os.environ.get(
    "SESSION_API_KEY_FILE", "/home/openhands/.openhands/agent-canvas/api-key.txt"
)
PAGE_LIMIT = int(os.environ.get("PAGE_LIMIT", "100"))
# A scrape walks the whole history, so it is not free. Serve a cached render
# when scraped more often than this.
MIN_SCRAPE_INTERVAL = float(os.environ.get("MIN_SCRAPE_INTERVAL_SECONDS", "25"))
HTTP_TIMEOUT = float(os.environ.get("HTTP_TIMEOUT_SECONDS", "20"))

logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format="%(asctime)s %(levelname)s %(message)s",
)
logger = logging.getLogger("openhands-metrics")

TOKEN_KINDS = (
    "prompt_tokens",
    "completion_tokens",
    "cache_read_tokens",
    "cache_write_tokens",
    "reasoning_tokens",
)


class ScrapeError(Exception):
    """The agent server could not be read. Surfaced as a metric, not a 500."""


def read_api_key() -> str | None:
    try:
        with open(API_KEY_FILE, encoding="utf-8") as handle:
            return handle.read().strip() or None
    except OSError as exc:
        logger.warning("could not read %s: %s", API_KEY_FILE, exc)
        return None


def _get(path: str, params: dict[str, object], api_key: str | None) -> dict:
    url = f"{AGENT_SERVER_URL.rstrip('/')}{path}?{urllib.parse.urlencode(params)}"
    request = urllib.request.Request(url, headers={"Accept": "application/json"})
    if api_key:
        request.add_header("X-Session-API-Key", api_key)
    try:
        with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT) as response:
            return json.loads(response.read().decode("utf-8"))
    except (urllib.error.URLError, OSError, json.JSONDecodeError) as exc:
        raise ScrapeError(f"GET {path}: {exc}") from exc


def collect(api_key: str | None) -> dict:
    """Walk every conversation page and fold the usage figures together."""
    by_status: dict[str, int] = defaultdict(int)
    cost: dict[str, float] = defaultdict(float)
    tokens: dict[tuple[str, str], int] = defaultdict(int)
    total = 0
    page_id = None

    while True:
        params: dict[str, object] = {"limit": PAGE_LIMIT}
        if page_id:
            params["page_id"] = page_id
        payload = _get("/api/conversations/search", params, api_key)

        for conversation in payload.get("items") or []:
            total += 1
            by_status[str(conversation.get("execution_status") or "unknown")] += 1
            usage = (conversation.get("stats") or {}).get("usage_to_metrics") or {}
            for entry in usage.values():
                model = str(entry.get("model_name") or "unknown")
                cost[model] += float(entry.get("accumulated_cost") or 0.0)
                accumulated = entry.get("accumulated_token_usage") or {}
                for kind in TOKEN_KINDS:
                    tokens[(model, kind)] += int(accumulated.get(kind) or 0)

        page_id = payload.get("next_page_id")
        if not page_id:
            break

    return {
        "total": total,
        "by_status": dict(by_status),
        "cost": dict(cost),
        "tokens": {f"{m}\x00{k}": v for (m, k), v in tokens.items()},
    }


def escape(value: str) -> str:
    return value.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def render(snapshot: dict, scrape_errors: int, scraped_at: float) -> str:
    lines: list[str] = []

    lines.append("# HELP openhands_conversations Conversations the agent server holds.")
    lines.append("# TYPE openhands_conversations gauge")
    for status, count in sorted(snapshot.get("by_status", {}).items()):
        lines.append(f'openhands_conversations{{status="{escape(status)}"}} {count}')

    lines.append(
        "# HELP openhands_conversations_total Conversations across every status."
    )
    lines.append("# TYPE openhands_conversations_total gauge")
    lines.append(f"openhands_conversations_total {snapshot.get('total', 0)}")

    lines.append(
        "# HELP openhands_usage_cost_usd Accumulated model spend over the "
        "conversations still held, by model."
    )
    lines.append("# TYPE openhands_usage_cost_usd gauge")
    for model, value in sorted(snapshot.get("cost", {}).items()):
        lines.append(f'openhands_usage_cost_usd{{model="{escape(model)}"}} {value:.6f}')

    lines.append(
        "# HELP openhands_usage_tokens Accumulated tokens over the conversations "
        "still held, by model and kind."
    )
    lines.append("# TYPE openhands_usage_tokens gauge")
    for key, value in sorted(snapshot.get("tokens", {}).items()):
        model, kind = key.split("\x00", 1)
        kind = kind.removesuffix("_tokens")
        lines.append(
            f'openhands_usage_tokens{{model="{escape(model)}",'
            f'kind="{escape(kind)}"}} {value}'
        )

    lines.append(
        "# HELP openhands_metrics_scrape_errors_total Failed reads of the agent "
        "server since this exporter started."
    )
    lines.append("# TYPE openhands_metrics_scrape_errors_total counter")
    lines.append(f"openhands_metrics_scrape_errors_total {scrape_errors}")

    lines.append(
        "# HELP openhands_metrics_last_success_timestamp_seconds When the figures "
        "above were last refreshed."
    )
    lines.append("# TYPE openhands_metrics_last_success_timestamp_seconds gauge")
    lines.append(f"openhands_metrics_last_success_timestamp_seconds {scraped_at:.3f}")

    return "\n".join(lines) + "\n"


class Collector:
    """Holds the last good snapshot so a scrape never blocks on a slow walk."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._snapshot: dict = {"total": 0, "by_status": {}, "cost": {}, "tokens": {}}
        self._scraped_at = 0.0
        self._errors = 0

    def render(self) -> str:
        with self._lock:
            stale = time.time() - self._scraped_at >= MIN_SCRAPE_INTERVAL
        if stale:
            try:
                snapshot = collect(read_api_key())
            except ScrapeError as exc:
                # Keep serving the previous snapshot: a blip in the agent server
                # should show up as a stale timestamp and an error count, not as
                # a gap in every series.
                logger.warning("scrape failed: %s", exc)
                with self._lock:
                    self._errors += 1
            else:
                with self._lock:
                    self._snapshot = snapshot
                    self._scraped_at = time.time()
        with self._lock:
            return render(self._snapshot, self._errors, self._scraped_at)


class Handler(BaseHTTPRequestHandler):
    collector: Collector

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler's spelling
        if self.path.startswith("/healthz"):
            self._respond(200, "text/plain", "ok\n")
            return
        if self.path.startswith("/metrics"):
            self._respond(
                200, "text/plain; version=0.0.4; charset=utf-8", self.collector.render()
            )
            return
        self._respond(404, "text/plain", "not found\n")

    def _respond(self, status: int, content_type: str, body: str) -> None:
        encoded = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, fmt: str, *args: object) -> None:
        logger.debug("%s - %s", self.address_string(), fmt % args)


def main() -> int:
    Handler.collector = Collector()
    server = ThreadingHTTPServer(("", PORT), Handler)
    logger.info("serving /metrics on :%s from %s", PORT, AGENT_SERVER_URL)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
