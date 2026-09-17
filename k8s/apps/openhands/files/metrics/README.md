# metrics

Prometheus exporter for agent session token and cost usage. Runs as a sidecar
beside the agent server; see the chart's `README.md`, "Metrics".

The agent server records per-conversation usage under
`stats.usage_to_metrics.<profile>` but exposes no `/metrics` endpoint, and
there is no upstream usage dashboard — this walks
`GET /api/conversations/search` and renders what is there.

## Series

| Metric | Labels |
| --- | --- |
| `openhands_conversations` | `status` |
| `openhands_conversations_total` | — |
| `openhands_usage_cost_usd` | `model` |
| `openhands_usage_tokens` | `model`, `kind` |
| `openhands_metrics_scrape_errors_total` | — |
| `openhands_metrics_last_success_timestamp_seconds` | — |

`kind` is `prompt`, `completion`, `cache_read`, `cache_write` or `reasoning`.

**Everything except the error count is a gauge**, including the series whose
names read like totals. They are recomputed from the conversations that exist
right now, so deleting one makes the number go down — which a counter may not
do. Chart them with `max_over_time`, not `rate`.

A failed read keeps the previous snapshot and increments
`openhands_metrics_scrape_errors_total`, so an agent-server blip shows up as a
stale `..._last_success_timestamp_seconds` rather than a hole in every series.

## Tests

```sh
uv run --no-cache --link-mode copy pytest
```
