#!/usr/bin/env bash
# Terminal node. Prints one JSON object describing what the run produced.
#
# Reached only when the ci-loop completed, which by `until_bash` means the last
# await-ci reported success — so `green` is true here by construction. It is
# still emitted explicitly so a consumer reads a fact rather than inferring one
# from the run's status.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=summary
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

branch="$(cat "$ARTIFACTS_DIR/branch" 2>/dev/null || branch_name)"
head_sha="$(cat "$ARTIFACTS_DIR/head-sha" 2>/dev/null || true)"
pr_number="$(cat "$ARTIFACTS_DIR/pr-number" 2>/dev/null || true)"
attempts="$(cat "$ARTIFACTS_DIR/attempt" 2>/dev/null || echo 0)"

pr_url=""
[ -n "$pr_number" ] && pr_url="${FORGEJO_URL%/}/${FORGEJO_OWNER}/${FORGEJO_REPO}/pulls/${pr_number}"

log "PR #${pr_number:-?} on $branch @ ${head_sha:-?} is green after $attempts sandbox attempt(s)"

jq -nc \
  --argjson green true \
  --arg pr "${pr_number:-}" \
  --arg pr_url "$pr_url" \
  --arg branch "$branch" \
  --arg head "${head_sha:-}" \
  --arg attempts "$attempts" \
  '{green: $green, pr: $pr, pr_url: $pr_url, branch: $branch, head: $head, attempts: $attempts}'
