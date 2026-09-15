#!/usr/bin/env bash
# Runs INSIDE the sandbox pod, as both the clone init container and the agent
# container. Mounted read-only from the archon-workflow ConfigMap, so editing it
# means editing k8s/apps/archon/files/workflow/agent-entrypoint.sh and letting
# ArgoCD roll the Deployment.
#
# Modes:
#   clone           init container: fetch the repo into $WORKDIR/repo at $REPO_REF
#   implement|fix   agent container: run Claude Code, commit, push $BRANCH
#
# The pod's exit code is the attempt's verdict — the agent-sandbox controller
# turns it into the Sandbox's `Finished` condition (PodSucceeded / PodFailed),
# which is the only signal the Archon side reads. So every failure path here
# must exit non-zero, and the happy path must exit 0 having actually pushed.
set -euo pipefail

log() { printf '[agent] %s\n' "$*" >&2; }
die() { printf '[agent] FATAL: %s\n' "$*" >&2; exit 1; }

mode="${1:?usage: entrypoint.sh clone|implement|fix}"

# ---------------------------------------------------------------------------
# clone
# ---------------------------------------------------------------------------
if [ "$mode" = clone ]; then
  : "${REPO_CLONE_URL:?}" "${REPO_REF:?}" "${WORKDIR:?}"

  # $HOME for the agent container lives on this same volume; create it here,
  # while we still have a writable tree and before git wants somewhere to put
  # its config.
  mkdir -p "$WORKDIR/home" "$WORKDIR/artifacts"

  if [ -d "$WORKDIR/repo/.git" ]; then
    # Only reachable if the controller restarted the init container against a
    # retained PVC. Re-cloning is cheaper than reasoning about what state the
    # partial tree is in.
    log "stale checkout present, removing"
    rm -rf "$WORKDIR/repo"
  fi

  log "cloning $REPO_CLONE_URL at $REPO_REF"
  # --no-single-branch: the agent needs the base branch too, to diff against
  # and to rebase onto. Unauthenticated — the repo is public; push credentials
  # exist only in the agent container.
  git clone --no-single-branch --branch "$REPO_REF" "$REPO_CLONE_URL" "$WORKDIR/repo"
  log "clone complete: $(git -C "$WORKDIR/repo" rev-parse --short HEAD)"
  exit 0
fi

# ---------------------------------------------------------------------------
# implement | fix
# ---------------------------------------------------------------------------
case "$mode" in
  implement | fix) ;;
  *) die "unknown mode '$mode'" ;;
esac

: "${REPO_DIR:?}" "${TASK_DIR:?}" "${BRANCH:?}" "${BASE_BRANCH:?}" "${HOME:?}"
: "${REPO_PUSH_URL:?}" "${FORGEJO_USERNAME:?}" "${FORGEJO_TOKEN:?}"
[ -d "$REPO_DIR/.git" ] || die "no checkout at $REPO_DIR — the clone init container did not run"
[ -r "$TASK_DIR/task.md" ] || die "no task at $TASK_DIR/task.md"

mkdir -p "$HOME"
cd "$REPO_DIR"

git config user.name "${GIT_AUTHOR_NAME:-Archon Agent}"
git config user.email "${GIT_AUTHOR_EMAIL:-archon@msng.to}"
git config --global --add safe.directory "$REPO_DIR"

# Credential helper rather than a token in the remote URL: git writes remote
# URLs into .git/config and echoes them in error messages, and the URL would
# then also be visible to anything the agent runs.
git config credential.helper \
  '!f() { printf "username=%s\npassword=%s\n" "$FORGEJO_USERNAME" "$FORGEJO_TOKEN"; }; f'

log "mode=$mode branch=$BRANCH base=$BASE_BRANCH head=$(git rev-parse --short HEAD)"

if [ "$mode" = implement ]; then
  # Fresh branch off the base. `-B` so a re-run of the first attempt (e.g. after
  # a transient sandbox failure) is not blocked by a leftover local ref.
  git checkout -B "$BRANCH"
else
  # Fix attempt: the clone already checked out $BRANCH (REPO_REF was the branch,
  # not the base). Assert it, rather than assuming — a silent base-branch fix
  # would push the agent's work onto main.
  current="$(git rev-parse --abbrev-ref HEAD)"
  [ "$current" = "$BRANCH" ] || die "expected checkout of '$BRANCH', got '$current'"
fi

# ---------------------------------------------------------------------------
# Build the prompt
# ---------------------------------------------------------------------------
prompt_file="$(mktemp)"
{
  cat /opt/agent/prompt.md
  printf '\n## Branch\n\nYou are on `%s`, branched from `%s`.\n' "$BRANCH" "$BASE_BRANCH"
  printf '\n## Task\n\n'
  cat "$TASK_DIR/task.md"
  if [ "$mode" = fix ] && [ -r "$TASK_DIR/ci-log.txt" ]; then
    printf '\n## Failing CI output\n\n'
    printf 'The PR check for your previous commit failed. This is the tail of the\n'
    printf 'failing step log. Fix the cause; do not paper over it by disabling a\n'
    printf 'check or loosening a lint rule unless the task explicitly asks for that.\n\n'
    printf '```\n'
    cat "$TASK_DIR/ci-log.txt"
    printf '\n```\n'
  fi
} > "$prompt_file"

# ---------------------------------------------------------------------------
# Run the agent
# ---------------------------------------------------------------------------
# Headless, non-interactive — --dangerously-skip-permissions is what makes
# that possible with no human to approve tool calls. See README.md, "Security
# posture" for what bounds it instead.
log "running claude (model=${CLAUDE_MODEL:-default})"
set +e
claude \
  --print \
  --dangerously-skip-permissions \
  ${CLAUDE_MODEL:+--model "$CLAUDE_MODEL"} \
  < "$prompt_file"
claude_rc=$?
set -e
rm -f "$prompt_file"
[ "$claude_rc" -eq 0 ] || die "claude exited $claude_rc"

# ---------------------------------------------------------------------------
# Commit and push
# ---------------------------------------------------------------------------
git add -A
if git diff --cached --quiet; then
  # Not a "nothing to do" success: the caller asked for a change and the branch
  # is unchanged, so there is nothing to open a PR for and nothing for CI to
  # check. Fail loudly rather than looping on an empty branch.
  die "agent produced no changes"
fi

subject="$([ "$mode" = implement ] && echo "Archon: $BRANCH" || echo "Archon: fix CI on $BRANCH")"
git commit --no-verify -m "$subject" -m "Generated by the Archon homelab-sandbox-pr workflow, mode=$mode."

log "pushing $BRANCH"
git push --set-upstream origin "HEAD:refs/heads/$BRANCH"

sha="$(git rev-parse HEAD)"
log "pushed $sha"
# Stdout is the pod log, which the driver tails. One machine-readable line.
printf 'ARCHON_PUSHED_SHA=%s\n' "$sha"
