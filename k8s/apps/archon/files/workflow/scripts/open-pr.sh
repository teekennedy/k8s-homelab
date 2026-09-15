#!/usr/bin/env bash
# Opens the pull request for the branch the implement attempt pushed, and prints
# its number to stdout.
#
# Idempotent: if a PR for this head branch is already open (a retried node, or a
# re-run against a branch that survived a failed run), it is reused rather than
# duplicated — Forgejo would reject the second create anyway, with a 409 that
# says nothing useful.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=open-pr
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

BRANCH="$(branch_name)"
BASE_BRANCH="$(base_branch)"
head_sha="$(cat "$ARTIFACTS_DIR/head-sha" 2>/dev/null || true)"
[ -n "$head_sha" ] || die "no head SHA recorded — did run-agent.sh implement run?"

# Reuse an existing open PR for this head branch.
existing="$(forgejo_api GET "$(repo_path)/pulls?state=open&limit=50" \
  | jq -r --arg b "$BRANCH" '.[] | select(.head.ref == $b) | .number' | head -n 1)"
if [ -n "$existing" ]; then
  log "reusing open PR #$existing for $BRANCH"
  printf '%s\n' "$existing" > "$ARTIFACTS_DIR/pr-number"
  printf '%s\n' "$existing"
  exit 0
fi

title="$(printf '%s' "${ARGUMENTS:-Automated change}" | head -n 1 | cut -c1-72)"
[ -n "$title" ] || title="Automated change"

body_file="$(mktemp)"
trap 'rm -f "$body_file"' EXIT
{
  printf 'Opened by the Archon `homelab-sandbox-pr` workflow.\n\n'
  printf '## Task\n\n'
  printf '%s\n\n' "${ARGUMENTS:-}"
  printf '## How this was produced\n\n'
  printf -- '- Claude Code ran in a single-use `agents.x-k8s.io/v1beta1` Sandbox in the `%s` namespace.\n' "$SANDBOX_NAMESPACE"
  printf -- '- The sandbox had a read-only, Secret-free view of the cluster and no write access to anything.\n'
  printf -- '- Archon run `%s`.\n\n' "$WORKFLOW_ID"
  printf 'If the PR check fails, the workflow starts a fresh sandbox on this branch\n'
  printf 'with the failing log attached and pushes a fix, up to the ci-loop'"'"'s\n'
  printf '`max_iterations` bound.\n\n'
  printf -- '---\n\n'
  printf 'Review this like any other PR. It was written by a model, unattended.\n'
} > "$body_file"

payload="$(jq -n \
  --arg title "$title" \
  --arg body "$(cat "$body_file")" \
  --arg head "$BRANCH" \
  --arg base "$BASE_BRANCH" \
  '{title: $title, body: $body, head: $head, base: $base}')"

number="$(forgejo_api POST "$(repo_path)/pulls" "$payload" | jq -r '.number')"
[ -n "$number" ] && [ "$number" != null ] || die "PR create returned no number"

log "opened PR #$number ($BRANCH -> $BASE_BRANCH) at $head_sha"
printf '%s\n' "$number" > "$ARTIFACTS_DIR/pr-number"
printf '%s\n' "$number"
