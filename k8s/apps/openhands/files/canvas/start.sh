#!/usr/bin/env bash
# Canvas pod entrypoint, in place of the image's /opt/agent-canvas/entrypoint.sh,
# which has no way to pass --lock-to-cloud to the static server. Ported from
# that script (agent-canvas 1.20.0); what it did that no longer applies here is
# left out rather than disabled: the bundled agent server, the editor route,
# the public-mode server, and the session key baked into the page.
#
#   :8000   static frontend, locked to the app server's cloud API
#   :18001  automation service, in cloud mode against the app server
set -uo pipefail

log() { printf '[canvas] %s\n' "$*"; }

# shellcheck source=/dev/null
[ -f /opt/agent-canvas/defaults.env ] && . /opt/agent-canvas/defaults.env

: "${APP_SERVER_URL:?must be set}"
: "${LOCK_TO_CLOUD:?must be set}"
: "${LOCAL_BACKEND_API_KEY:?must be set}"
: "${APP_SERVER_SERVICE_KEY:?must be set}"

PORT="${PORT:-8000}"
AUTOMATION_PORT=18001

OPENHANDS_DIR="${HOME}/.openhands"
STATE_DIR="${OPENHANDS_DIR}/agent-canvas"
mkdir -p "$STATE_DIR"
export OH_PERSISTENCE_DIR="${OPENHANDS_DIR}"
export OH_CONVERSATIONS_PATH="${STATE_DIR}/conversations"
export OH_BASH_EVENTS_DIR="${STATE_DIR}/bash_events"

# Decrypts the agent server's secret store: generated once, then kept on the
# data volume.
SECRET_KEY_FILE="${STATE_DIR}/secret-key.txt"
if [ ! -f "$SECRET_KEY_FILE" ]; then
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$SECRET_KEY_FILE"
  chmod 600 "$SECRET_KEY_FILE"
  log "Generated OH_SECRET_KEY"
fi
OH_SECRET_KEY="$(cat "$SECRET_KEY_FILE")"
export OH_SECRET_KEY

# The key the automation service accepts from the app server, from a Secret
# the app server also reads.
KEY="$LOCAL_BACKEND_API_KEY"
export OH_SESSION_API_KEYS_0="$KEY"
export OPENHANDS_AUTOMATION_API_KEY="$KEY"
export AUTOMATION_KV_SECRET="$KEY"
export OPENHANDS_REMOTE_WS_READY_REQUIRED=false

if [ "${VITE_DO_NOT_TRACK:-}" = "1" ]; then
  export DO_NOT_TRACK=1
fi

# Cloud mode, which is what leaving AUTOMATION_AGENT_SERVER_URL unset selects:
# every run gets a sandbox of its own from the app server, and every caller is
# checked against the app server's /api/v1/users/me.
export AUTOMATION_OPENHANDS_API_BASE_URL="$APP_SERVER_URL"
export AUTOMATION_SERVICE_KEY="$APP_SERVER_SERVICE_KEY"
# Persisted conversations may name the legacy canvas_ui_tool module.
export OH_EXTRA_PYTHON_PATH=/opt/agent-canvas/tools
# Nothing routes to the editor any more.
export OH_ENABLE_VSCODE=false

PIDS=()
cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  wait 2>/dev/null || true
  exit 0
}
trap cleanup SIGINT SIGTERM

export FILE_STORE=local
export LOCAL_STORAGE_PATH="${OPENHANDS_DIR}/storage"
export AUTOMATION_WORKSPACE_BASE="${OPENHANDS_DIR}/workspaces"
export AUTOMATION_DB_URL="${AUTOMATION_DB_URL:-sqlite+aiosqlite:///${OPENHANDS_DIR}/automation/automations.db}"
mkdir -p "$LOCAL_STORAGE_PATH" "$AUTOMATION_WORKSPACE_BASE" "${OPENHANDS_DIR}/automation"

log "Starting automation service on :${AUTOMATION_PORT}"
uvicorn openhands.automation.app:app --host 0.0.0.0 --port "$AUTOMATION_PORT" &
PIDS+=($!)

# The agent profile form shows the frontend's built-in Claude Code command,
# which pins the adapter version it was built against. Serve a copy that names
# the version sandboxes run instead; see patch_frontend.py.
FRONTEND_DIR=/opt/agent-canvas/frontend
if [ -n "${CLAUDE_ADAPTER_VERSION:-}" ]; then
  FRONTEND_DIR="$(python3 /etc/openhands-canvas/patch_frontend.py \
    "$FRONTEND_DIR" /tmp/frontend "$CLAUDE_ADAPTER_VERSION")" || FRONTEND_DIR=/opt/agent-canvas/frontend
  log "Serving the frontend from ${FRONTEND_DIR}"
fi

# The app server serves /api, /runtime, and — authenticated — the automation
# API. A forge webhook carries no session, so its delivery path goes straight
# to the automation service, which checks the HMAC itself.
log "Starting frontend on :${PORT}, locked to ${LOCK_TO_CLOUD}"
node /opt/agent-canvas/static-server.mjs \
  --port "$PORT" \
  --host :: \
  --dir "$FRONTEND_DIR" \
  --base-path /canvas \
  --lock-to-cloud "$LOCK_TO_CLOUD" \
  --route "/api/automation/v1/events=http://127.0.0.1:${AUTOMATION_PORT}" \
  --route "/api=${APP_SERVER_URL}" \
  --route "/runtime=${APP_SERVER_URL}" &
STATIC_PID=$!
PIDS+=("$STATIC_PID")

# Backends that crash are tolerated (the proxy answers 502); the pod lives as
# long as the frontend does.
while kill -0 "$STATIC_PID" 2>/dev/null; do
  sleep 10 &
  wait $!
done
log "Static server exited"
exit 1
