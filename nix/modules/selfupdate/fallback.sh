# Body of nixos-selfupdate.service, the fallback timer's target.
#
# CI drives nixos-selfupdate-build.service and nixos-selfupdate-stage.service
# directly across the fleet (see docs/nixos-cd.md). This timer has no fleet to
# coordinate with -- it is one host, alone, noticing CI has not reached it in a
# while -- so it just chains the two units back to back on itself.
#
# Runs daily but does nothing unless the last successful stage was over
# STALENESS_SECONDS ago, which is sized just past the weekly cadence of
# Renovate's flake-update PRs: this only acts in a week where the CI trigger
# was missed entirely.

age_seconds() {
  echo $(($(date +%s) - $(stat -c %Y "$STAMP_FILE")))
}

if [ -e "$STAMP_FILE" ]; then
  age=$(age_seconds)
  if [ "$age" -lt "$STALENESS_SECONDS" ]; then
    echo "last successful update was ${age}s ago, under the ${STALENESS_SECONDS}s fallback threshold; nothing to do"
    exit 0
  fi
  echo "last successful update was ${age}s ago; running the fallback update"
else
  echo "no successful update recorded yet; running the fallback update"
fi

status=0
"$SYSTEMCTL" start --wait nixos-selfupdate-build.service || status=$?
if [ "$status" -ne 0 ]; then
  echo "error: build failed; not staging" >&2
  exit "$status"
fi

if [ ! -e "$BUILT_REV_FILE" ]; then
  echo "error: build succeeded but $BUILT_REV_FILE is missing" >&2
  exit 1
fi
cat "$BUILT_REV_FILE" >"$STAGE_TARGET_FILE"

"$SYSTEMCTL" start --wait nixos-selfupdate-stage.service
