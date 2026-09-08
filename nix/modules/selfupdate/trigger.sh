# Forced command for the deploybot SSH login: sshd's `Match User deploybot`
# block sets ForceCommand to this script, so it runs no matter what the client
# asks for. That same block denies a pty, agent/port/X11 forwarding and
# tunnels, and sets AuthorizedKeysFile to none -- a certificate signed by the
# SSH CA is the only credential that gets in.
#
# The only input is $SSH_ORIGINAL_COMMAND. Four verbs, nothing else, and
# everything privileged goes through one of three literal, argument-for-argument
# sudoers entries (no wildcards).

usage() {
  cat >&2 <<'EOF'
usage: pass one of these as the ssh remote command

  build <40-hex-commit>   build that commit, without staging it
  stage <40-hex-commit>   promote this host's build of that commit for next boot
  reboot                  create the kured sentinel if a new generation is staged
  status                  report booted/staged/built generations and last update time
EOF
  exit 64
}

# Runs one privileged unit for a requested commit, working around
# `systemctl start --wait` joining an already-running instance of the unit
# rather than queueing a new one: joining returns when *that* run finishes -- a
# run that read its own commit before ours was ever written, and whose log
# would then describe a run we did not ask for. Overlapping pipelines make this
# routine, and reporting someone else's run as this one's success is how a host
# ends up staging a commit nobody asked it to.
#
# $1: the target-rev file to write before starting the unit; consumed by the
#     unit, so it still existing afterwards is exactly the signal that our
#     request was not the one that ran.
# $2: the sudo argv to run (unquoted below on purpose: this is the exact argv
#     the corresponding sudoers entry lists, built from the same nix binding,
#     so the two cannot drift).
# $3: the rev being requested, for the retry/error messages.
# $4: the log file the unit tees its own output to.
run_privileged() {
  local target_file=$1 cmd=$2 rev=$3 log=$4
  local attempt=0 status

  printf '%s\n' "$rev" >"$target_file"

  while :; do
    attempt=$((attempt + 1))
    status=0
    # shellcheck disable=SC2086
    "$SUDO" -n $cmd || status=$?

    [ -f "$target_file" ] || break

    if [ "$attempt" -ge "$MAX_ATTEMPTS" ]; then
      echo "error: $rev was never picked up; joined another run on all $attempt attempts" >&2
      rm -f "$target_file"
      status=75
      break
    fi
    echo "joined a run already in progress; retrying ($attempt/$MAX_ATTEMPTS)" >&2
  done

  # The unit tees its own output here so that reading it back needs no
  # systemd-journal group membership, which would expose the whole journal.
  if [ -f "$log" ]; then
    cat "$log"
  fi
  return "$status"
}

# `read -a` splits on IFS without glob expansion, so nothing the client sends
# can expand into a path.
read -r -a argv <<<"${SSH_ORIGINAL_COMMAND:-}"
verb=${argv[0]:-}

case "$verb" in
build)
  [ "${#argv[@]}" -eq 2 ] || usage
  rev=${argv[1]}
  # The unit re-validates this and additionally requires the commit to be an
  # ancestor of the tracked branch. This check is just a fast, legible reject.
  if ! [[ "$rev" =~ ^[0-9a-f]{40}$ ]]; then
    echo "error: '$rev' is not a full commit sha" >&2
    exit 64
  fi
  run_privileged "$BUILD_TARGET_FILE" "$BUILD_CMD" "$rev" "$BUILD_LOG"
  exit $?
  ;;

stage)
  [ "${#argv[@]}" -eq 2 ] || usage
  rev=${argv[1]}
  if ! [[ "$rev" =~ ^[0-9a-f]{40}$ ]]; then
    echo "error: '$rev' is not a full commit sha" >&2
    exit 64
  fi
  run_privileged "$STAGE_TARGET_FILE" "$STAGE_CMD" "$rev" "$STAGE_LOG"
  exit $?
  ;;

reboot)
  [ "${#argv[@]}" -eq 1 ] || usage
  if [ "$(readlink -f /run/booted-system)" = "$(readlink -f /nix/var/nix/profiles/system)" ]; then
    # The rollout job keys off this exact word to skip a host without waiting
    # for a reboot that is never going to happen.
    echo UP_TO_DATE
    exit 0
  fi
  # shellcheck disable=SC2086  # as above: must match the sudoers argv
  "$SUDO" -n $SENTINEL_CMD
  echo REBOOT_PENDING
  ;;

status)
  [ "${#argv[@]}" -eq 1 ] || usage
  echo "host:    $(uname -n)"
  echo "booted:  $(readlink -f /run/booted-system)"
  echo "staged:  $(readlink -f /nix/var/nix/profiles/system)"
  if [ -e "$BUILT_REV_FILE" ]; then
    echo "built:   $(cat "$BUILT_REV_FILE")"
  fi
  if [ -e "$LAST_REV_FILE" ]; then
    echo "rev:     $(cat "$LAST_REV_FILE")"
  fi
  if [ -e "$STAMP_FILE" ]; then
    mtime=$(stat -c %Y "$STAMP_FILE")
    echo "updated: $(date -Is -d "@$mtime") ($((($(date +%s) - mtime) / 3600))h ago)"
  else
    echo "updated: never"
  fi
  ;;

*)
  usage
  ;;
esac
