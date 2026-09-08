# Body of nixos-selfupdate-stage.service. Runs as root, oneshot.
#
# Promotes a build this host already made -- BUILT_SYSTEM_LINK, recorded
# alongside the commit it was built from in BUILT_REV_FILE -- to
# /nix/var/nix/profiles/system and activates it for the next boot. This never
# builds or fetches anything of its own: that already happened in
# nixos-selfupdate-build.service (see build.sh). Keeping the two separate lets
# a caller stage only once a build has actually succeeded, without staging
# ever having to wait on a rebuild.
#
# The only input is TARGET_REV_FILE, written by the caller before starting this
# unit -- the deploybot trigger (CI) or the fallback timer, exactly as
# build.sh's TARGET_REV_FILE. It exists purely to name which build to promote;
# it does not grant the caller any choice of code, since the promoted path was
# already built and its rev already validated by build.sh.

main() {
  local target last

  if [ ! -f "$TARGET_REV_FILE" ]; then
    echo "error: nothing to stage; $TARGET_REV_FILE was not written" >&2
    return 1
  fi
  target=$(cat "$TARGET_REV_FILE")
  rm -f "$TARGET_REV_FILE"

  if [ ! -e "$BUILT_REV_FILE" ] || [ "$(cat "$BUILT_REV_FILE")" != "$target" ]; then
    echo "error: $target is not what this host last built (built: $(cat "$BUILT_REV_FILE" 2>/dev/null || echo none)); refusing to stage" >&2
    return 1
  fi
  if [ ! -e "$BUILT_SYSTEM_LINK" ]; then
    echo "error: $BUILT_SYSTEM_LINK is missing; nothing to promote" >&2
    return 1
  fi

  # Belt and suspenders: build.sh already refused to move backwards before
  # producing this build, but re-check here too, since staging is the step that
  # actually changes what boots next. A git failure (an unknown commit after a
  # force-push, say) fails towards staging rather than silently doing nothing,
  # same as the equivalent check in build.sh.
  if [ -e "$LAST_REV_FILE" ]; then
    last=$(cat "$LAST_REV_FILE")
    if [ "$target" != "$last" ] &&
      git -C "$REPO_DIR" merge-base --is-ancestor "$target" "$last" 2>/dev/null; then
      echo "$target is an ancestor of the already-staged commit $last; refusing to move backwards"
      return 0
    fi
  fi

  local system_path
  system_path=$(readlink -f "$BUILT_SYSTEM_LINK")
  echo "staging $system_path (rev $target)"

  # This is what `nixos-rebuild boot` itself does once it has a built toplevel:
  # point the system profile at it and run its own activation script with
  # `boot`, which wires it up for the next boot without switching now.
  nix-env --profile /nix/var/nix/profiles/system --set "$system_path"
  "$system_path/bin/switch-to-configuration" boot

  printf '%s\n' "$target" >"$LAST_REV_FILE"
  touch "$STAMP_FILE"

  local booted staged
  booted=$(readlink -f /run/booted-system)
  staged=$(readlink -f /nix/var/nix/profiles/system)
  if [ "$booted" = "$staged" ]; then
    echo "staged generation matches the booted one; no reboot needed"
  else
    echo "staged $staged for next boot (booted: $booted)"
  fi
}

# See build.sh for why this is piped through tee rather than exec'd through
# process substitution: the trigger cats RUN_LOG the instant `systemctl start
# --wait` returns, and process substitution would race that.
main 2>&1 | tee "$RUN_LOG"
