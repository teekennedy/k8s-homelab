#!/usr/bin/env bash
# The ci-loop's `until_bash`: exit 0 when the commit the last wait covered went
# green, non-zero to go round again.
#
# Reads the verdict file rather than the wait node's structured output. The file
# is written before the signal is sent, so by the time this runs it is there;
# and reading it keeps the loop's exit condition independent of how Archon
# happens to substitute a nested field into a shell string.
# shellcheck disable=SC2034  # consumed by log() in common.sh
SCRIPT_NAME=ci-green
# shellcheck source=./common.sh
. "${WORKFLOW_DIR:?}/scripts/common.sh"

head_sha="$(current_head_sha)"
[ -n "$head_sha" ] || { log "no head sha recorded yet"; exit 1; }

verdict="$(ci_verdict_file "$head_sha")"
if [ ! -s "$verdict" ]; then
  # The wait expired without a notification. ci-verdict.sh fails the run on the
  # next iteration, where it can say so properly; here, just keep looping.
  log "no verdict for $head_sha yet"
  exit 1
fi

status="$(jq -r '.status // empty' < "$verdict")"
log "verdict for $head_sha: ${status:-unknown}"
[ "$status" = success ]
