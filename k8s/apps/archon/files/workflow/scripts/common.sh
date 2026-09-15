#!/usr/bin/env bash
# Sourced by every node script. Runs in the Archon container, NOT in a sandbox.
#
# A bash node's stdout IS its `$node.output` (one trailing newline stripped),
# compared literally by `when:` / `until_bash`. So every diagnostic in these
# scripts goes to stderr, and each script prints at most one bare token to
# stdout.
set -euo pipefail

log() { printf '[%s] %s\n' "${SCRIPT_NAME:-archon}" "$*" >&2; }
die() { printf '[%s] FATAL: %s\n' "${SCRIPT_NAME:-archon}" "$*" >&2; exit 1; }

require_env() {
  local missing=()
  local name
  for name in "$@"; do
    [ -n "${!name:-}" ] || missing+=("$name")
  done
  [ ${#missing[@]} -eq 0 ] || die "missing required environment: ${missing[*]}"
}

require_env ARTIFACTS_DIR WORKFLOW_ID WORKFLOW_DIR \
  FORGEJO_URL FORGEJO_OWNER FORGEJO_REPO FORGEJO_TOKEN \
  SANDBOX_NAMESPACE

# Archon creates $ARTIFACTS_DIR per run; the nodes share state through it.
mkdir -p "$ARTIFACTS_DIR"

# Short, DNS-safe run id. Object names derive from it, so it has to survive
# being a label value and a k8s name segment.
run_id() {
  printf '%s' "$WORKFLOW_ID" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | cut -c1-12
}

branch_name() {
  printf '%s/%s' "${FORGEJO_BRANCH_PREFIX:-agent}" "$(run_id)"
}

base_branch() {
  printf '%s' "${BASE_BRANCH:-${FORGEJO_DEFAULT_BRANCH:-main}}"
}

# ---------------------------------------------------------------------------
# Templating
# ---------------------------------------------------------------------------
# render_template <file> <VAR>... — substitute ${NAME} placeholders named by
# the given variables, then fail if any are left (unlike envsubst, which would
# silently replace a typo'd placeholder with the empty string).
render_template() {
  local tpl="$1"; shift
  local content name
  content="$(cat "$tpl")"
  for name in "$@"; do
    content="${content//\$\{$name\}/${!name:-}}"
  done
  case "$content" in
    *'${'*)
      printf '%s\n' "$content" | grep -n '\${' >&2 || true
      die "unsubstituted placeholder(s) in $tpl (see above)"
      ;;
  esac
  printf '%s\n' "$content"
}

# ---------------------------------------------------------------------------
# Forgejo API
# ---------------------------------------------------------------------------
# $1 = method, $2 = path under /api/v1, $3 = optional JSON body.
# Fails the calling node on any non-2xx, with the body on stderr — an API error
# swallowed here would surface much later as an inexplicable empty PR number.
forgejo_api() {
  local method="$1" path="$2" body="${3:-}"
  local url="${FORGEJO_URL%/}/api/v1${path}"
  local out status
  out="$(mktemp)"
  local -a args=(
    --silent --show-error --location
    --max-time 60
    --request "$method"
    --header "Authorization: token ${FORGEJO_TOKEN}"
    --header 'Accept: application/json'
    --write-out '%{http_code}'
    --output "$out"
  )
  if [ -n "$body" ]; then
    args+=(--header 'Content-Type: application/json' --data-binary "$body")
  fi
  status="$(curl "${args[@]}" "$url")" || {
    rm -f "$out"
    die "forgejo $method $path: curl failed"
  }
  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    log "forgejo $method $path -> HTTP $status"
    cat "$out" >&2 || true
    rm -f "$out"
    die "forgejo $method $path failed"
  fi
  cat "$out"
  rm -f "$out"
}

repo_path() { printf '/repos/%s/%s' "$FORGEJO_OWNER" "$FORGEJO_REPO"; }

# Head commit SHA of a branch, or empty if the branch does not exist.
branch_head_sha() {
  local branch="$1" out status url
  url="${FORGEJO_URL%/}/api/v1$(repo_path)/branches/$(printf '%s' "$branch" | sed 's;/;%2F;g')"
  out="$(mktemp)"
  status="$(curl --silent --show-error --location --max-time 60 \
    --header "Authorization: token ${FORGEJO_TOKEN}" \
    --header 'Accept: application/json' \
    --write-out '%{http_code}' --output "$out" "$url")" || { rm -f "$out"; die "branch lookup failed"; }
  if [ "$status" = 404 ]; then
    rm -f "$out"
    return 0
  fi
  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    cat "$out" >&2 || true
    rm -f "$out"
    die "branch lookup for '$branch' returned HTTP $status"
  fi
  jq -r '.commit.id // empty' < "$out"
  rm -f "$out"
}

# ---------------------------------------------------------------------------
# Run state
# ---------------------------------------------------------------------------
# The branch and commit the loop is currently about, shared between nodes
# through $ARTIFACTS_DIR: run-agent.sh records them after every push, arm-ci.sh
# before every wait.
record_head() {
  printf '%s\n' "$1" > "$ARTIFACTS_DIR/branch"
  printf '%s\n' "$2" > "$ARTIFACTS_DIR/head-sha"
}

current_branch() { cat "$ARTIFACTS_DIR/branch" 2>/dev/null || branch_name; }
current_head_sha() { cat "$ARTIFACTS_DIR/head-sha" 2>/dev/null || true; }

# ---------------------------------------------------------------------------
# CI events
# ---------------------------------------------------------------------------
# Where these scripts and the ci-signal relay sidecar meet; both mount the
# Archon data volume. Correlation is by commit SHA because that is the only
# identifier Woodpecker's notification and this workflow both already know —
# and a 40-char hex string needs no escaping to be used as a filename.
CI_EVENTS_DIR="${CI_EVENTS_DIR:-/.archon/ci-events}"

# ci_wait_file <sha> — the arming record, written here, read by the relay.
ci_wait_file() { printf '%s/waits/%s.json' "$CI_EVENTS_DIR" "$1"; }

# ci_verdict_file <sha> — the pipeline result, written by the relay, read here.
ci_verdict_file() { printf '%s/verdicts/%s.json' "$CI_EVENTS_DIR" "$1"; }

# write_atomic <path> — read stdin, then replace <path> in one rename. The relay
# polls these directories, so a half-written file would be read as a real one.
write_atomic() {
  local target="$1" staging
  mkdir -p "$(dirname "$target")"
  staging="$(mktemp "$(dirname "$target")/.tmp.XXXXXX")"
  cat > "$staging"
  mv -f "$staging" "$target"
}

# ---------------------------------------------------------------------------
# kubectl
# ---------------------------------------------------------------------------
# Always namespace-scoped: the ServiceAccount has no cluster-wide grants, so an
# accidental cluster-scoped call fails with a confusing Forbidden instead of an
# obvious one.
kc() { kubectl --namespace "$SANDBOX_NAMESPACE" "$@"; }
