# Writes node-exporter textfile metrics describing this host's self-update state.
#
# This runs on its own timer rather than at the end of an update, because
# reboot_pending has to fall back to 0 once the staged generation has actually
# booted -- and nothing runs the updater at that point.

booted=$(readlink -f /run/booted-system)
staged=$(readlink -f /nix/var/nix/profiles/system)

pending=0
[ "$booted" = "$staged" ] || pending=1

last_success=0
if [ -e "$STAMP_FILE" ]; then
  last_success=$(stat -c %Y "$STAMP_FILE")
fi

rev=""
if [ -e "$LAST_REV_FILE" ]; then
  rev=$(<"$LAST_REV_FILE")
fi

out="$TEXTFILE_DIR/nixos_selfupdate.prom"
tmp=$(mktemp "$out.XXXXXX")
trap 'rm -f "$tmp"' EXIT

{
  echo "# HELP nixos_selfupdate_last_success_timestamp_seconds Unix time of the last successful self-update run."
  echo "# TYPE nixos_selfupdate_last_success_timestamp_seconds gauge"
  echo "nixos_selfupdate_last_success_timestamp_seconds $last_success"
  echo "# HELP nixos_selfupdate_reboot_pending Whether the staged system generation differs from the booted one."
  echo "# TYPE nixos_selfupdate_reboot_pending gauge"
  echo "nixos_selfupdate_reboot_pending $pending"
  echo "# HELP nixos_selfupdate_info Commit and store paths of the most recent build."
  echo "# TYPE nixos_selfupdate_info gauge"
  echo "nixos_selfupdate_info{rev=\"$rev\",booted_system=\"$booted\",staged_system=\"$staged\"} 1"
} >"$tmp"

# node-exporter reads this as an unprivileged user in the DaemonSet's mount namespace.
chmod 0644 "$tmp"
mv "$tmp" "$out"
