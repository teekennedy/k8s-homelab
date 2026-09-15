#!/usr/bin/env bash
# First node of each ci-loop iteration. Reports on the wait that the PREVIOUS
# iteration ended with, and prints exactly one token to stdout:
#
#   none      nothing has been waited on yet — this is the first iteration
#   success   the pipeline for the current head commit went green
#   failure   it did not; $ARTIFACTS_DIR/ci-failure.log now holds the evidence
#
# This replaces the old await-ci.sh poll loop. The waiting is now the `await-ci`
# wait node's job, released by the ci-signal relay; what is left here is reading
# the verdict and, on a red build, pulling the failing step logs out of
# Woodpecker for the fix attempt.
#
# A red build is a SUCCESSFUL run of this script — the verdict is its stdout,
# not its exit code. It exits non-zero only when the verdict cannot be
# determined at all, which stops the run.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=ci-verdict
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

command -v jq >/dev/null 2>&1 || die "jq not found on PATH"

head_sha="$(current_head_sha)"
[ -n "$head_sha" ] || die "no head sha recorded — did run-agent.sh implement run?"

wait_record="$(ci_wait_file "$head_sha")"
verdict="$(ci_verdict_file "$head_sha")"

if [ ! -s "$verdict" ]; then
  if [ -s "$wait_record" ]; then
    # A wait was armed for this commit and the deadline passed without the
    # relay ever hearing from Woodpecker. Continuing would loop on a commit
    # nothing is going to report on, so stop and say why.
    die "the wait for $head_sha expired without a pipeline result — is the notify step in .woodpecker/ci.yaml reaching the ci-signal relay?"
  fi
  log "nothing has been waited on yet"
  printf 'none\n'
  exit 0
fi

status="$(jq -r '.status // empty' < "$verdict")"
[ -n "$status" ] || die "verdict file $verdict has no status"
log "CI verdict for $head_sha: $status"

if [ "$status" = success ]; then
  # A stale log would otherwise be handed to a fix attempt that has no business
  # running.
  rm -f "$ARTIFACTS_DIR/ci-failure.log"
  printf 'success\n'
  exit 0
fi

# ---------------------------------------------------------------------------
# Woodpecker API
# ---------------------------------------------------------------------------
# Only reached on a red build, which is why the credentials are required here
# rather than at the top: a green run must not depend on them.
require_env WOODPECKER_URL WOODPECKER_TOKEN

woodpecker_api() {
  local path="$1" out status
  out="$(mktemp)"
  status="$(curl --silent --show-error --location --max-time 120 \
    --header "Authorization: Bearer ${WOODPECKER_TOKEN}" \
    --header 'Accept: application/json' \
    --write-out '%{http_code}' --output "$out" "${WOODPECKER_URL%/}/api${path}")" \
    || { rm -f "$out"; die "woodpecker GET $path: curl failed"; }
  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    log "woodpecker GET $path -> HTTP $status"
    cat "$out" >&2 || true
    rm -f "$out"
    die "woodpecker GET $path failed"
  fi
  cat "$out"
  rm -f "$out"
}

# ---------------------------------------------------------------------------
# Collect the failing logs
# ---------------------------------------------------------------------------
# Best-effort: a red build with no retrievable log is still a red build, and the
# fix attempt can work from the verdict alone. So everything below logs and
# continues rather than dying.
out="$ARTIFACTS_DIR/ci-failure.log"
: > "$out"

# The relay recorded both ids, so unlike the old poll loop there is no need to
# look the repo up or search the pipeline list for a matching commit.
repo_id="$(jq -r '.repo_id // empty' < "$verdict")"
pipeline_number="$(jq -r '.pipeline_number // empty' < "$verdict")"
pipeline_url="$(jq -r '.pipeline_url // empty' < "$verdict")"

collect_logs() {
  local pipeline steps step_id step_name entries

  [ -n "$repo_id" ] && [ -n "$pipeline_number" ] || { log "verdict names no pipeline"; return 1; }
  log "failing pipeline: ${pipeline_url:-${WOODPECKER_URL%/}/repos/${repo_id}/pipeline/${pipeline_number}}"
  printf 'Pipeline: %s\n\n' "${pipeline_url:-${WOODPECKER_URL%/}/repos/${repo_id}/pipeline/${pipeline_number}}" >> "$out"

  pipeline="$(woodpecker_api "/repos/${repo_id}/pipelines/${pipeline_number}")"
  steps="$(printf '%s' "$pipeline" | jq -c '
    [ .workflows[]? | .children[]?
      | select(.state == "failure" or .state == "error" or .state == "killed") ]
  ')"
  [ "$(printf '%s' "$steps" | jq 'length')" -gt 0 ] || { log "pipeline $pipeline_number has no failed step"; return 1; }

  while IFS=$'\t' read -r step_id step_name; do
    [ -n "$step_id" ] || continue
    log "collecting log for step '$step_name' (id $step_id)"
    printf '===== step: %s =====\n' "$step_name" >> "$out"
    # Log entries are {line, data} with data base64-encoded (Go []byte). Decode
    # per entry: the store may have interleaved stdout and stderr, and only the
    # line ordering is meaningful.
    entries="$(woodpecker_api "/repos/${repo_id}/logs/${pipeline_number}/${step_id}")" || continue
    printf '%s' "$entries" \
      | jq -r 'sort_by(.line) | .[] | .data' \
      | while IFS= read -r chunk; do printf '%s' "$chunk" | base64 -d 2>/dev/null || true; done \
      >> "$out"
    printf '\n' >> "$out"
  done < <(printf '%s' "$steps" | jq -r '.[] | [(.id|tostring), .name] | @tsv')
  return 0
}

if ! collect_logs; then
  log "falling back to the verdict as the only failure evidence"
  {
    printf 'Could not retrieve the Woodpecker step logs.\n\n'
    jq -r '"- status: \(.status)\n- branch: \(.branch)\n- commit: \(.commit)\n- pipeline: \(.pipeline_url // "unknown")"' < "$verdict"
  } >> "$out"
fi

# Keep only the tail: the ConfigMap that carries this into the sandbox is capped
# at 1 MiB by the API server, and the end of a build log is where the error is.
log_tail_bytes="${CI_LOG_TAIL_BYTES:-262144}"
if [ "$(wc -c < "$out")" -gt "$log_tail_bytes" ]; then
  tail -c "$log_tail_bytes" "$out" > "$out.tail"
  {
    printf '[truncated to the last %s bytes]\n\n' "$log_tail_bytes"
    cat "$out.tail"
  } > "$out"
  rm -f "$out.tail"
fi

log "wrote $(wc -c < "$out") bytes of failure log to $out"
printf 'failure\n'
