#!/usr/bin/env bash
# Arms the durable wait for whatever is currently at the head of the branch, and
# prints that SHA.
#
# Runs immediately before the `await-ci` wait node, which is the only thing in
# this workflow that blocks on CI. The wait is released by the ci-signal relay
# (k8s/apps/archon/files/ci-signal), which learns the Archon run id from the
# record written here — Woodpecker's notification carries a commit and nothing
# else, so this file is the whole correlation.
#
# The head is read from the server rather than from $ARTIFACTS_DIR/head-sha: on
# the second and later iterations the fix attempt has already pushed, and the
# pipeline that matters is the one for THAT commit.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=arm-ci
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

command -v jq >/dev/null 2>&1 || die "jq not found on PATH"

BRANCH="$(branch_name)"
head_sha="$(branch_head_sha "$BRANCH")"
[ -n "$head_sha" ] || die "branch '$BRANCH' is not on the server"

record_head "$BRANCH" "$head_sha"

# Written before the wait node starts, so a pipeline that finishes early still
# finds it. The relay retries the signal until the wait is actually open, which
# covers the other side of the same race.
jq -nc \
  --arg run_id "${WORKFLOW_ID:?}" \
  --arg event "${CI_SIGNAL_EVENT:-ci.complete}" \
  --arg commit "$head_sha" \
  --arg branch "$BRANCH" \
  '{run_id: $run_id, event: $event, commit: $commit, branch: $branch}' \
  | write_atomic "$(ci_wait_file "$head_sha")"

log "armed ${CI_SIGNAL_EVENT:-ci.complete} for $BRANCH @ $head_sha (run $WORKFLOW_ID)"
printf '%s\n' "$head_sha"
