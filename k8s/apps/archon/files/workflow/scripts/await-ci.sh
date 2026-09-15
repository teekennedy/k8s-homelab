#!/usr/bin/env bash
# Waits for the PR check on the branch's current head commit, and prints
# exactly one token to stdout: `success` or `failure`. Both are a SUCCESSFUL
# run of this script — a non-zero exit means the CI result could not be
# determined at all, which is different and should stop the run.
#
# On `failure` it also writes $ARTIFACTS_DIR/ci-failure.log, which the next fix
# attempt hands the agent.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=await-ci
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

require_env WOODPECKER_URL WOODPECKER_TOKEN CI_STATUS_CONTEXT

poll_interval="${CI_POLL_INTERVAL_SECONDS:-20}"
wait_timeout="${CI_WAIT_TIMEOUT_SECONDS:-2700}"
log_tail_bytes="${CI_LOG_TAIL_BYTES:-262144}"

BRANCH="$(branch_name)"

# The head is read from the server, not from $ARTIFACTS_DIR/head-sha: on the
# second and later loop iterations the fix attempt has already pushed, and the
# check we care about is the one for THAT commit.
head_sha="$(branch_head_sha "$BRANCH")"
[ -n "$head_sha" ] || die "branch '$BRANCH' is not on the server"
printf '%s\n' "$head_sha" > "$ARTIFACTS_DIR/head-sha"
log "waiting for CI on $BRANCH @ $head_sha (context prefix '$CI_STATUS_CONTEXT')"

# ---------------------------------------------------------------------------
# Woodpecker API
# ---------------------------------------------------------------------------
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
# Poll the commit status
# ---------------------------------------------------------------------------
# $CI_STATUS_CONTEXT is a prefix, not an exact context — every matching status
# has to be terminal before the commit is judged.
deadline=$(( $(date +%s) + wait_timeout ))
verdict=""
while :; do
  statuses="$(forgejo_api GET "$(repo_path)/commits/${head_sha}/statuses?limit=100")"

  # Latest status per context — Forgejo keeps the whole history, newest first.
  relevant="$(printf '%s' "$statuses" | jq -c --arg prefix "$CI_STATUS_CONTEXT" '
    [ .[] | select(.context | startswith($prefix)) ]
    | group_by(.context)
    | map(sort_by(.created_at) | last)
  ')"

  count="$(printf '%s' "$relevant" | jq 'length')"
  if [ "$count" -gt 0 ]; then
    pending="$(printf '%s' "$relevant" | jq '[ .[] | select(.status == "pending") ] | length')"
    bad="$(printf '%s' "$relevant" | jq '[ .[] | select(.status == "failure" or .status == "error") ] | length')"
    if [ "$pending" -eq 0 ]; then
      if [ "$bad" -gt 0 ]; then
        verdict=failure
      else
        verdict=success
      fi
      break
    fi
    log "$pending of $count check(s) still pending"
  else
    log "no '$CI_STATUS_CONTEXT*' status on $head_sha yet"
  fi

  if [ "$(date +%s)" -ge "$deadline" ]; then
    die "timed out after ${wait_timeout}s waiting for CI on $head_sha"
  fi
  sleep "$poll_interval"
done

printf '%s\n' "$relevant" > "$ARTIFACTS_DIR/ci-statuses.json"
log "CI verdict for $head_sha: $verdict"

if [ "$verdict" = success ]; then
  # Stale log from a previous iteration would otherwise be handed to a fix
  # attempt that has no business running.
  rm -f "$ARTIFACTS_DIR/ci-failure.log"
  printf 'success\n'
  exit 0
fi

# ---------------------------------------------------------------------------
# Collect the failing logs
# ---------------------------------------------------------------------------
# Best-effort: a red build with no retrievable log is still a red build, and the
# fix attempt can work from the status alone. So everything below logs and
# continues rather than dying.
out="$ARTIFACTS_DIR/ci-failure.log"
: > "$out"

collect_logs() {
  local repo_id pipeline_number pipeline steps step_id step_name entries

  repo_id="$(woodpecker_api "/repos/lookup/${FORGEJO_OWNER}/${FORGEJO_REPO}" | jq -r '.id // empty')"
  [ -n "$repo_id" ] || { log "could not look up Woodpecker repo id"; return 1; }

  # Newest pipeline for this commit on this branch. `?branch=` narrows the page
  # so the commit filter below does not have to walk the whole history.
  pipeline_number="$(woodpecker_api "/repos/${repo_id}/pipelines?branch=$(printf '%s' "$BRANCH" | sed 's;/;%2F;g')&perPage=50" \
    | jq -r --arg sha "$head_sha" '[ .[] | select(.commit == $sha) ] | sort_by(.number) | last | .number // empty')"
  [ -n "$pipeline_number" ] || { log "no Woodpecker pipeline found for $head_sha"; return 1; }
  log "failing pipeline: ${WOODPECKER_URL%/}/repos/${repo_id}/pipeline/${pipeline_number}"
  printf 'Pipeline: %s/repos/%s/pipeline/%s\n\n' "${WOODPECKER_URL%/}" "$repo_id" "$pipeline_number" >> "$out"

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
  log "falling back to the commit statuses as the only failure evidence"
  {
    printf 'Could not retrieve the Woodpecker step logs. Failing checks:\n\n'
    printf '%s' "$relevant" | jq -r '.[] | "- \(.context): \(.status) \(.target_url // "")"'
  } >> "$out"
fi

# Keep only the tail: the ConfigMap that carries this into the sandbox is capped
# at 1 MiB by the API server, and the end of a build log is where the error is.
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
