#!/usr/bin/env bash
# One agent attempt, start to finish.
#
#   run-agent.sh implement   first pass: branch off $BASE_BRANCH, do the task
#   run-agent.sh fix         later pass: check out the branch, fix the CI failure
#
# Creates a task ConfigMap and a Sandbox, waits for the Sandbox's `Finished`
# condition, copies the pod log into $ARTIFACTS_DIR, and deletes both. Prints the
# pushed commit SHA to stdout; everything else goes to stderr.
#
# Idempotence: object names include the attempt number, which is the count of
# previous attempts recorded in $ARTIFACTS_DIR — so a retried node creates a new
# Sandbox rather than colliding with the previous one's leftovers.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=run-agent
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

mode="${1:?usage: run-agent.sh implement|fix}"
case "$mode" in
  implement | fix) ;;
  *) die "unknown mode '$mode'" ;;
esac

require_env SANDBOX_IMAGE SANDBOX_SERVICE_ACCOUNT SANDBOX_ANTHROPIC_SECRET \
  SANDBOX_FORGE_SECRET SANDBOX_TIMEOUT_SECONDS SANDBOX_WORKDIR_SIZE \
  SANDBOX_POD_TEMPLATE FORGEJO_CLONE_URL

command -v kubectl >/dev/null 2>&1 || die "kubectl not found on PATH"
command -v jq >/dev/null 2>&1 || die "jq not found on PATH"

RUN_ID="$(run_id)"
BRANCH="$(branch_name)"
BASE_BRANCH="$(base_branch)"

# Attempt counter. Kept in $ARTIFACTS_DIR so it survives the loop_group's
# iterations (each body node is a fresh process).
attempt_file="$ARTIFACTS_DIR/attempt"
ATTEMPT=$(( $(cat "$attempt_file" 2>/dev/null || echo 0) + 1 ))
printf '%s\n' "$ATTEMPT" > "$attempt_file"

SANDBOX_NAME="archon-${RUN_ID}-${ATTEMPT}"
TASK_CONFIGMAP="archon-task-${RUN_ID}-${ATTEMPT}"
WORKFLOW_CONFIGMAP="${WORKFLOW_CONFIGMAP:-archon-workflow}"

# implement clones the base branch; fix clones the feature branch the previous
# attempt pushed.
if [ "$mode" = implement ]; then
  REPO_REF="$BASE_BRANCH"
else
  REPO_REF="$BRANCH"
  [ -n "$(branch_head_sha "$BRANCH")" ] || die "fix attempt but branch '$BRANCH' does not exist on the server"
fi

log "attempt $ATTEMPT: mode=$mode branch=$BRANCH ref=$REPO_REF sandbox=$SANDBOX_NAME"

# ---------------------------------------------------------------------------
# Task ConfigMap
# ---------------------------------------------------------------------------
# $ARGUMENTS is the user's trigger message, delivered as a real env var. It can
# be anything, so it goes in through a file rather than a shell argument.
task_dir="$(mktemp -d)"
cleanup_task_dir() { rm -rf "$task_dir"; }
trap cleanup_task_dir EXIT

printf '%s\n' "${ARGUMENTS:-}" > "$task_dir/task.md"
[ -s "$task_dir/task.md" ] || die "empty task: this workflow needs a prompt describing the change"

cm_args=(--from-file="task.md=$task_dir/task.md")
if [ "$mode" = fix ]; then
  ci_log="$ARTIFACTS_DIR/ci-failure.log"
  [ -s "$ci_log" ] || die "fix attempt but no CI log at $ci_log — await-ci.sh should have written it"
  # await-ci.sh already truncated to $CI_LOG_TAIL_BYTES; this is the belt to that
  # braces, because a ConfigMap over 1 MiB is rejected by the API server and
  # would fail the attempt for a reason that looks nothing like its cause.
  tail -c "${CI_LOG_TAIL_BYTES:-262144}" "$ci_log" > "$task_dir/ci-log.txt"
  cm_args+=(--from-file="ci-log.txt=$task_dir/ci-log.txt")
fi

kc delete configmap "$TASK_CONFIGMAP" --ignore-not-found >&2
kc create configmap "$TASK_CONFIGMAP" "${cm_args[@]}" >&2

# ---------------------------------------------------------------------------
# Sandbox
# ---------------------------------------------------------------------------
# Deleting the Sandbox cascades to its pod and its work PVC (the controller owns
# both). The trap covers the timeout and failure paths too — a leaked Sandbox
# holds a Longhorn volume and a model-sized pod indefinitely.
cleanup() {
  local rc=$?
  cleanup_task_dir
  log "cleaning up $SANDBOX_NAME"
  kc delete sandbox "$SANDBOX_NAME" --ignore-not-found --wait=false >&2 || true
  kc delete configmap "$TASK_CONFIGMAP" --ignore-not-found --wait=false >&2 || true
  return $rc
}
trap cleanup EXIT

# shellcheck disable=SC2034  # consumed by name in the render_template call below
AGENT_MODE="$mode"
SANDBOX_CPU_REQUEST="${SANDBOX_CPU_REQUEST:-500m}"
SANDBOX_MEMORY_REQUEST="${SANDBOX_MEMORY_REQUEST:-1Gi}"
SANDBOX_CPU_LIMIT="${SANDBOX_CPU_LIMIT:-4}"
SANDBOX_MEMORY_LIMIT="${SANDBOX_MEMORY_LIMIT:-8Gi}"
SANDBOX_MODEL="${SANDBOX_MODEL:-}"

manifest="$ARTIFACTS_DIR/sandbox-${ATTEMPT}.yaml"
render_template "$SANDBOX_POD_TEMPLATE" \
  SANDBOX_NAME SANDBOX_NAMESPACE SANDBOX_IMAGE SANDBOX_MODEL \
  SANDBOX_SERVICE_ACCOUNT SANDBOX_ANTHROPIC_SECRET SANDBOX_FORGE_SECRET \
  SANDBOX_TIMEOUT_SECONDS SANDBOX_WORKDIR_SIZE \
  SANDBOX_CPU_REQUEST SANDBOX_MEMORY_REQUEST SANDBOX_CPU_LIMIT SANDBOX_MEMORY_LIMIT \
  TASK_CONFIGMAP WORKFLOW_CONFIGMAP AGENT_MODE \
  RUN_ID ATTEMPT BRANCH BASE_BRANCH REPO_REF FORGEJO_CLONE_URL \
  > "$manifest"
kc apply -f "$manifest" >&2

# `kubectl wait` needs the object to exist and the condition to be present;
# `Finished` is only added once the pod reaches a terminal phase, so this is the
# single call that covers scheduling, cloning, the model run, and the push.
# --timeout is the sandbox budget plus slack for image pull and PVC provisioning.
wait_timeout=$(( SANDBOX_TIMEOUT_SECONDS + 600 ))
log "waiting up to ${wait_timeout}s for $SANDBOX_NAME to finish"
wait_rc=0
kc wait --for=condition=Finished "sandbox/$SANDBOX_NAME" --timeout="${wait_timeout}s" >&2 || wait_rc=$?

# Collect the log before deciding anything: on the failure path it is the only
# evidence, and the EXIT trap is about to delete the pod.
pod_name="$(kc get "sandbox/$SANDBOX_NAME" \
  -o jsonpath='{.metadata.annotations.agents\.x-k8s\.io/pod-name}' 2>/dev/null || true)"
[ -n "$pod_name" ] || pod_name="$SANDBOX_NAME"
agent_log="$ARTIFACTS_DIR/agent-${ATTEMPT}.log"
kc logs "pod/$pod_name" --container agent --tail=-1 > "$agent_log" 2>/dev/null \
  || log "could not read agent log from pod/$pod_name"
kc logs "pod/$pod_name" --container clone --tail=-1 > "$ARTIFACTS_DIR/clone-${ATTEMPT}.log" 2>/dev/null || true

if [ "$wait_rc" -ne 0 ]; then
  log "--- last 50 lines of the agent log ---"
  tail -n 50 "$agent_log" >&2 2>/dev/null || true
  die "sandbox $SANDBOX_NAME did not finish within ${wait_timeout}s"
fi

reason="$(kc get "sandbox/$SANDBOX_NAME" \
  -o jsonpath='{.status.conditions[?(@.type=="Finished")].reason}' 2>/dev/null || true)"
if [ "$reason" != "PodSucceeded" ]; then
  log "--- last 50 lines of the agent log ---"
  tail -n 50 "$agent_log" >&2 2>/dev/null || true
  die "agent attempt $ATTEMPT failed (Finished reason: ${reason:-unknown})"
fi

# ---------------------------------------------------------------------------
# Result
# ---------------------------------------------------------------------------
# The agent pushed, so the server is the source of truth for the SHA. The
# ARCHON_PUSHED_SHA line in the pod log is a cross-check: if they disagree,
# something else moved the branch between the push and now, and continuing would
# make the workflow chase a commit its agent did not write.
pushed_sha="$(grep -oE '^ARCHON_PUSHED_SHA=[0-9a-f]{40}$' "$agent_log" | tail -n 1 | cut -d= -f2 || true)"
server_sha="$(branch_head_sha "$BRANCH")"
[ -n "$server_sha" ] || die "agent reported success but branch '$BRANCH' is not on the server"
if [ -n "$pushed_sha" ] && [ "$pushed_sha" != "$server_sha" ]; then
  die "branch '$BRANCH' is at $server_sha but the agent pushed $pushed_sha — concurrent write?"
fi

printf '%s\n' "$server_sha" > "$ARTIFACTS_DIR/head-sha"
printf '%s\n' "$BRANCH" > "$ARTIFACTS_DIR/branch"
log "attempt $ATTEMPT pushed $server_sha to $BRANCH"
printf '%s\n' "$server_sha"
