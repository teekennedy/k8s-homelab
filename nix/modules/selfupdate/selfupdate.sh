# Body of nixos-selfupdate.service. Runs as root, oneshot.
#
# Fetches the tracked branch, builds this host's NixOS configuration from a
# specific commit, and stages it for the next boot. It does not create the kured
# reboot sentinel file. That is handled by a separate unit, driven by the
# in-cluster rollout job so that reboots happen in a controlled order.
#
# Two callers, distinguished only by whether TARGET_REV_FILE exists:
#   * the deploybot trigger (CI), which writes the commit to it first
#   * nixos-selfupdate.timer (fallback), which does not
# The file is consumed on read, so a later timer run in the same boot cannot
# re-deploy a stale pinned commit.

fetch() {
  local url
  local -a urls
  read -r -a urls <<<"$FLAKE_URLS"

  if [ ! -d "$REPO_DIR" ]; then
    echo "initialising mirror at $REPO_DIR"
    git init --quiet --bare "$REPO_DIR"
  fi

  # Remotes are tried in order. The forge runs on this very cluster, so the
  # public mirror is what lets a host still update itself when the cluster is
  # down -- which is exactly when you would want it to.
  for url in "${urls[@]}"; do
    echo "fetching $BRANCH from $url"
    git -C "$REPO_DIR" fetch --quiet --prune "$url" "+refs/heads/$BRANCH:refs/heads/$BRANCH" && return 0
    echo "warning: fetch from $url failed"
  done

  echo "error: could not fetch $BRANCH from any configured remote" >&2
  return 1
}

# Echoes the commit to build. Enforces the trust boundary: a caller may only
# select a commit that is already an ancestor of the tracked branch, so a
# compromised CI can at worst ask for an older commit that is already on the
# branch. It can never introduce code of its own.
resolve_rev() {
  local requested=$1

  if [ -z "$requested" ]; then
    git -C "$REPO_DIR" rev-parse "refs/heads/$BRANCH"
    return 0
  fi

  if ! [[ "$requested" =~ ^[0-9a-f]{40}$ ]]; then
    echo "error: '$requested' is not a full commit sha" >&2
    return 1
  fi
  if ! git -C "$REPO_DIR" merge-base --is-ancestor "$requested" "refs/heads/$BRANCH"; then
    echo "error: $requested is not an ancestor of $BRANCH" >&2
    return 1
  fi
  echo "$requested"
}

main() {
  local rev="" age target flake_ref booted staged

  if [ -f "$TARGET_REV_FILE" ]; then
    rev=$(cat "$TARGET_REV_FILE")
    rm -f "$TARGET_REV_FILE"
    echo "commit requested by trigger: $rev"
  elif [ -e "$STAMP_FILE" ]; then
    age=$(($(date +%s) - $(stat -c %Y "$STAMP_FILE")))
    if [ "$age" -lt "$STALENESS_SECONDS" ]; then
      echo "last successful update was ${age}s ago, under the ${STALENESS_SECONDS}s fallback threshold; nothing to do"
      return 0
    fi
    echo "last successful update was ${age}s ago; running the fallback update"
  else
    echo "no successful update recorded yet; running the fallback update"
  fi

  fetch
  target=$(resolve_rev "$rev")
  echo "building $ATTRIBUTE at $target"

  # A git+file ref rather than a plain path, so that self.rev and
  # self.lastModifiedDate resolve and system.nixos.label carries the real short
  # rev instead of falling through to "dirty".
  flake_ref="git+file://$REPO_DIR?ref=refs/heads/$BRANCH&rev=$target"
  nixos-rebuild boot --flake "$flake_ref#$ATTRIBUTE"

  printf '%s\n' "$target" >"$LAST_REV_FILE"
  touch "$STAMP_FILE"

  booted=$(readlink -f /run/booted-system)
  staged=$(readlink -f /nix/var/nix/profiles/system)
  if [ "$booted" = "$staged" ]; then
    echo "staged generation matches the booted one; no reboot needed"
  else
    echo "staged $staged for next boot (booted: $booted)"
  fi
}

# Piped rather than `exec > >(tee ...)` so the shell waits for tee to flush:
# the trigger cats this log the instant `systemctl start --wait` returns, and
# process substitution races that. Output still reaches the journal via tee's
# own stdout.
main 2>&1 | tee "$RUN_LOG"
