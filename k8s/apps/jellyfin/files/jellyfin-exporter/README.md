# jellyfin-exporter

Prometheus exporter for Jellyfin.

## Goals

1. Track jellyfin streaming usage and transcoding performance.
2. Block kured node reboots while there are active streams.
3. Inform configuration and maintenance decisions.

## Metrics

| Metric | Notes |
| --- | --- |
| `jellyfin_active_streams` | playing and **not paused** |
| `jellyfin_paused_streams` | media loaded but paused |
| `jellyfin_active_video_transcodes` | unpaused sessions re-encoding video |
| `jellyfin_active_audio_transcodes` | unpaused sessions re-encoding audio |
| `jellyfin_session_info` | per-session labels, always `1` |
| `jellyfin_up` | whether the last Jellyfin query succeeded |
| `jellyfin_exporter_last_success_timestamp_seconds` | freshness |

`jellyfin_session_info` carries `user`, `client`, `device`, `play_method`,
`paused`, `video_transcode`, `audio_transcode`, `hardware_acceleration` and
`transcode_reasons`. It deliberately omits the item name: titles churn
constantly and would make the series unbounded.

## Reboot gating

`jellyfin_active_streams` backs the `JellyfinStreamActive` alert which blocks
kured from rebooting a node.

That alert notifies nobody, by design: it is a gate, and somebody watching a
film is not news. Alertmanager routes it to the `null` receiver alongside
`ResticBackupRunning` (see `alertmanager.config.route` in
`k8s/platform/monitoring-system/values.yaml`). kured reads firing alerts from
Prometheus directly, not from Alertmanager, so dropping the notification does
not weaken the gate — and unlike a silence, it stays in git and never expires.

Paused sessions are excluded on purpose. If a stream is paused it will remain
active until the user closes the app and Jellyfin's idle-reaper reaps the
session, or until the stream has been paused for `InactiveSessionThreshold`
minutes. `InactiveSessionThreshold` defaults to 0, which would keep paused
sessions active indefinitely.

If the exporter cannot reach Jellyfin it reports zeroes with `jellyfin_up 0`,
so an exporter outage lets reboots proceed rather than blocking them forever.

## How it gets its data

A WebSocket to Jellyfin's `/socket`, subscribed with `SessionsStart`, plus a
60-second `GET /Sessions` reconcile for drift correction and as a fallback when
the socket flaps. State expires after `STALE_AFTER` seconds so a silently dead
socket cannot pin the reboot gate open.

## How it runs

`exporter.py` is shipped as a ConfigMap and run with `uv run --script` on
`ghcr.io/astral-sh/uv:python3.14-alpine`. Its dependencies live in the PEP 723
header at the top of the file; keep that pin and `pyproject.toml` in step, since
the tests resolve against the latter.

The consequence worth knowing: a pod start fetches `websockets` from PyPI, so
the exporter cannot start while PyPI is unreachable. That degrades to
`jellyfin_up` absent, which *permits* node reboots rather than blocking them —
see `JellyfinExporterDown` below.

## Deploying it, or not

| Value | Effect |
| --- | --- |
| `app-template.controllers.exporter.enabled` | The exporter itself. Its ConfigMap, ServiceMonitor and alert rules follow it. Also flip `service.exporter`, `persistence.jellyfin-exporter` and `persistence.jellyfin-exporter-project` — a subchart's values cannot be templated from the parent. |
| `monitoring.exporter.serviceMonitor.enabled` | Prometheus scrapes the exporter. |
| `monitoring.exporter.prometheusRule.enabled` | `JellyfinStreamActive` / `JellyfinStuckSession` / `JellyfinExporterDown`. Turning this off leaves node reboots ungated. |

## Configuration

| Env var | Default | Purpose |
| --- | --- | --- |
| `JELLYFIN_URL` | *(required)* | e.g. `http://jellyfin-main:8096` |
| `JELLYFIN_API_KEY` | *(required)* | Jellyfin API key |
| `LISTEN_PORT` | `9101` | metrics port |
| `RECONCILE_INTERVAL` | `60` | REST poll interval, seconds |
| `STALE_AFTER` | `max(90, interval × 2.5)` | state expiry, seconds |
| `DEVICE_ID` | `jellyfin-exporter` | shown in Jellyfin's device list |
| `LOG_LEVEL` | `INFO` | |

### API key

Jellyfin API keys live in Jellyfin's own database and can only be created
through the admin UI, so this one cannot be generated declaratively the way
`api-keys` is. Create it once, by hand:

1. Jellyfin → Dashboard → API Keys → **+**, name it `jellyfin-exporter`.
2. ```sh
   kubectl -n jellyfin create secret generic jellyfin-exporter-api-key \
     --from-literal=JELLYFIN_API_KEY=<the key>
   ```

## Running tests

```sh
cd k8s/apps/jellyfin/files/jellyfin-exporter
uv sync --dev
uv run pytest
```

That runs everything, integration tests included. They exercise the exporter
against the stub Jellyfin server in `tests/conftest.py`, which serves both
`GET /Sessions` and the `/socket` WebSocket, so they need no cluster and run in
CI unattended.

To point the same tests at a real server instead:

```sh
kubectl -n jellyfin port-forward svc/jellyfin-main 8096:8096

export JELLYFIN_URL=http://localhost:8096
export JELLYFIN_API_KEY=<key>
uv run pytest -m integration
```

Setting both variables switches the `jellyfin_target` fixture over; unset, it
falls back to the stub. The integration tests are read-only — they query
sessions and open a WebSocket, and never touch playback — so they are safe to
run against a live Jellyfin deployment.
