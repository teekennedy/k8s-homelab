# Forced command for the deploybot SSH login: sshd's `Match User deploybot`
# block sets ForceCommand to this script, so it runs no matter what the client
# asks for. That same block denies a pty, agent/port/X11 forwarding and
# tunnels, and sets AuthorizedKeysFile to none -- a certificate signed by the
# SSH CA is the only credential that gets in.
#
# The only input is $SSH_ORIGINAL_COMMAND. Three verbs, nothing else, and
# everything privileged goes through one of two literal, argument-for-argument
# sudoers entries (no wildcards).

usage() {
  cat >&2 <<'EOF'
usage: pass one of these as the ssh remote command

  deploy <40-hex-commit>   build that commit and stage it for the next boot
  reboot                   create the kured sentinel if a new generation is staged
  status                   report booted/staged generations and last update time
EOF
  exit 64
}

# `read -a` splits on IFS without glob expansion, so nothing the client sends
# can expand into a path.
read -r -a argv <<<"${SSH_ORIGINAL_COMMAND:-}"
verb=${argv[0]:-}

case "$verb" in
deploy)
  [ "${#argv[@]}" -eq 2 ] || usage
  rev=${argv[1]}
  # The unit re-validates this and additionally requires the commit to be an
  # ancestor of the tracked branch. This check is just a fast, legible reject.
  if ! [[ "$rev" =~ ^[0-9a-f]{40}$ ]]; then
    echo "error: '$rev' is not a full commit sha" >&2
    exit 64
  fi
  printf '%s\n' "$rev" >"$TARGET_REV_FILE"

  status=0
  # Deliberately unquoted: this is the exact argv the sudoers entry lists, built
  # from the same nix binding, so the two cannot drift.
  # shellcheck disable=SC2086
  "$SUDO" -n $SELFUPDATE_CMD || status=$?
  # The unit tees its own output here so that reading it back needs no
  # systemd-journal group membership, which would expose the whole journal.
  if [ -f "$RUN_LOG" ]; then
    cat "$RUN_LOG"
  fi
  exit "$status"
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
