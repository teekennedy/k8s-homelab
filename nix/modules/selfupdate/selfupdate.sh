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
  local rev="" age target last flake_ref booted staged started status follower

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

  # Never move backwards. resolve_rev only proves the commit is *on* the branch,
  # not that it is newer than what this host already built, and the order
  # requests arrive in is not the order the commits were made: pipelines for
  # consecutive pushes overlap, get cancelled, get restarted by hand, and their
  # builds outlive the pipeline that asked for them. Without this, whichever
  # request lands last wins, which is how the fleet ends up staging a commit
  # older than the one it is already running.
  #
  # Deliberate rollback is still possible, it just has to be deliberate: remove
  # LAST_REV_FILE first, or run nixos-rebuild by hand.
  if [ -e "$LAST_REV_FILE" ]; then
    last=$(cat "$LAST_REV_FILE")
    # A git failure here (an unknown commit after a force-push, say) is a
    # non-zero exit like any other, so an unanswerable question fails towards
    # building rather than towards silently doing nothing.
    if [ "$target" != "$last" ] &&
      git -C "$REPO_DIR" merge-base --is-ancestor "$target" "$last"; then
      echo "$target is an ancestor of the last built commit $last; refusing to move backwards"
      return 0
    fi
  fi

  echo "building $ATTRIBUTE at $target"

  # A git+file ref rather than a plain path, so that self.rev and
  # self.lastModifiedDate resolve and system.nixos.label carries the real short
  # rev instead of falling through to "dirty".
  flake_ref="git+file://$REPO_DIR?ref=refs/heads/$BRANCH&rev=$target"

  # json-log-path tees Nix's internal-json event stream to a file while leaving
  # the human-readable output on stderr untouched (applyJSONLogger in
  # libutil/logging.cc wraps the existing logger rather than replacing it), so
  # this costs the log above nothing. It goes through NIX_CONFIG rather than a
  # flag because nixos-rebuild spawns several nix processes and every one of
  # them has to write to the same file; they append, which is what makes one
  # log per run possible -- and why it is truncated first, or it would grow
  # without bound across deploys.
  #
  # The metrics this feeds are counts, not timings: the event stream carries no
  # timestamps at all, so duration is measured out here around the whole
  # rebuild.
  : >"$NIX_LOG_JSON"
  export NIX_CONFIG="json-log-path = $NIX_LOG_JSON"

  # Nothing in the event stream is timestamped, so a follower reads the log as
  # it is written and records when each activity's start and stop arrived. It
  # tails a regular file rather than having Nix write to a named pipe on
  # purpose: Nix blocks in open() on a FIFO until a reader shows up, so a
  # follower that is late or dead would hang the build outright, and one that
  # stops draining would hang it as soon as the pipe buffer filled. Tailing a
  # file cannot do either -- the worst a broken follower costs is timings.
  "$METRICS_CMD" timings --json-log "$NIX_LOG_JSON" --output "$NIX_TIMINGS" &
  follower=$!

  started=$(date +%s)
  status=0
  nixos-rebuild boot --flake "$flake_ref#$ATTRIBUTE" || status=$?
  unset NIX_CONFIG

  # SIGTERM asks the follower to drain what is left and exit; the build is over
  # by now, so the tail of the log holds exactly the stop events it came for.
  kill -TERM "$follower" 2>/dev/null || true
  wait "$follower" 2>/dev/null || true

  # Never let metrics fail a deploy. A broken exporter must cost us numbers,
  # not a host.
  #
  # --output goes to the state dir, which is under /var/cache and so survives
  # the reboot this deploy is about to cause; --publish puts the same bytes in
  # the textfile collector directory, which does not. A tmpfiles rule copies
  # the state dir file back into place on the next boot. Only this file gets
  # that treatment: the timer-driven writers there rewrite themselves every
  # minute or five, and persisting their output would hide a broken one behind
  # a stale reading.
  "$METRICS_CMD" report \
    --json-log "$NIX_LOG_JSON" \
    --timings "$NIX_TIMINGS" \
    --output "$BUILD_METRICS_FILE" \
    --publish "$TEXTFILE_DIR/nixos_selfupdate_build.prom" \
    --duration-seconds "$(($(date +%s) - started))" \
    --exit-status "$status" ||
    echo "warning: build metrics export failed" >&2

  if [ "$status" -ne 0 ]; then
    return "$status"
  fi

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
#
# The grep drops git's transfer progress, which is otherwise several hundred
# lines of "remote: Counting objects: n%" per run in the pipeline log. It has to
# be filtered by name rather than turned off at the source: Nix fetches the
# flake by shelling out to `git fetch --progress --force` with the child's
# stderr inherited (libfetchers/git-utils.cc), so --progress is hardcoded -- git
# reports progress even though no terminal is attached -- and the output never
# passes through Nix's logger, which leaves `nix --quiet` and every flake
# setting powerless over it. Discarding stderr wholesale is not the answer
# either: it is the same stream as stdout by now, and it is where nixos-rebuild
# reports the failures worth reading.
#
# tr first, because git separates progress updates with carriage returns rather
# than newlines. `|| true` guards the pipeline against a grep that matched
# nothing; pipefail still carries a failure in main past both filters, since it
# reports the rightmost non-zero status and neither filter can fail on its own.
main 2>&1 |
  tr '\r' '\n' |
  { grep --line-buffered -Ev '^(remote: (Enumerating|Counting|Compressing|Total) objects|Receiving objects:|Resolving deltas:|Unpacking objects:)' || true; } |
  tee "$RUN_LOG"
