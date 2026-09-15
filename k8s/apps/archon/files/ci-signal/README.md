# ci-signal

A sidecar in the Archon pod that turns a Woodpecker pipeline notification into
an Archon durable-wait signal, so the `homelab-sandbox-pr` workflow can block on
CI instead of polling it.

See the "The CI loop" section of [the chart README](../../README.md) for the
diagram and the reasoning; this file is the operational detail.

## Why it exists

Woodpecker has no outgoing webhooks, so the notification is a step in
`.woodpecker/ci.yaml` and this is what receives it.

It is a sidecar rather than its own Deployment because it needs both halves of
the Archon pod: `/.archon` is ReadWriteOnce and shared with the workflow's bash
nodes, and Archon's signal API is reached over **loopback**, which is the only
place `ARCHON_WEB_AUTH_HEADER` can be trusted.

## Contract

`POST /ci`, authenticated by `X-CI-Signature: <hex HMAC-SHA256 of the raw body>`:

```json
{
  "commit": "40-char hex sha",
  "status": "success | failure | error | killed | declined",
  "branch": "agent/…",
  "pipeline_number": "42",
  "pipeline_url": "https://ci.msng.to/repos/1/pipeline/42",
  "repo_id": "1"
}
```

Returns `202` once the verdict is on disk; the signal itself is delivered on a
background thread. `GET /healthz` is the probe.

`commit` is validated against `^[0-9a-f]{40}$` before it is used as a filename —
the signature proves the sender holds the shared secret, not that the body is
sane.

## State

Both directories live on the Archon data volume, under `CI_SIGNAL_STATE_DIR`:

| path | written by | read by |
| --- | --- | --- |
| `waits/<sha>.json` | `arm-ci.sh` | this relay |
| `verdicts/<sha>.json` | this relay | `ci-verdict.sh`, `ci-green.sh` |

Verdicts are written to a temporary file and renamed, because `ci-green.sh`
parses one while the relay may be rewriting it. Records older than
`CI_SIGNAL_PRUNE_AFTER_SECONDS` are dropped on each request.

## Timing

Two races, both handled by the delivery loop:

- **The pipeline finishes before the wait node is reached.** The relay retries
  until `GET /api/workflows/runs/<id>` reports an open `ci.complete` wait, up to
  `CI_SIGNAL_DEADLINE_SECONDS` — which is deliberately longer than the wait's
  own `deadline_ms`.
- **The commit belongs to no Archon run.** Every `pull_request` pipeline on the
  repo notifies, not just the agent's branches, so a commit with no arming
  record after `CI_SIGNAL_ARM_GRACE_SECONDS` is dropped rather than retried.

`resumeAt` is read fresh from the run's metadata at signal time and passed back
unchanged. Archon compares it exactly, which is what stops a retry from
satisfying a later loop iteration.

## Tests

```sh
uv run pytest
```

Stdlib only at runtime — pytest is the single dev dependency.
