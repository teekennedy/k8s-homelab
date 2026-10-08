#!/bin/sh
# Replaces the engine image's /usr/local/bin/dagger-entrypoint.sh.
#
# Nests BuildKit child cgroups under the engine pod's own cgroup. K3s does not
# give privileged pods their own cgroup namespace, so the upstream dind-style
# cgroup setup ends up touching the host root cgroup and runc creates child
# cgroups at /sys/fs/cgroup/buildkit/<id>, escaping the kubepods.slice memory
# limit. See https://github.com/dagger/dagger/discussions/12703
set -e

ENGINE_CGROUP=$(awk -F: '$1=="0"{print $3}' /proc/self/cgroup)
if [ -n "$ENGINE_CGROUP" ] && [ "$ENGINE_CGROUP" != "/" ] && [ -d "/sys/fs/cgroup$ENGINE_CGROUP" ]; then
  POD_DIR="/sys/fs/cgroup$ENGINE_CGROUP"
  echo "dagger-cgroup-fix: nesting buildkit children under $ENGINE_CGROUP" >&2
  mkdir -p "$POD_DIR/init"
  # Move existing processes (this shell, soon-to-be dagger) into the
  # init leaf so the parent scope can have controllers in subtree_control.
  while read -r pid; do
    [ -n "$pid" ] && echo "$pid" > "$POD_DIR/init/cgroup.procs" 2>/dev/null || true
  done < "$POD_DIR/cgroup.procs"
  sed -e 's/ / +/g' -e 's/^/+/' < "$POD_DIR/cgroup.controllers" \
    > "$POD_DIR/cgroup.subtree_control" 2>/dev/null || true
  # Merge defaultCgroupParent into the engine TOML config so spawned
  # containers end up at <pod-scope>/buildkit/<id> and inherit
  # the pod memory.max. If [worker.oci] already exists in the
  # config, insert the key under it; otherwise append a new section.
  mkdir -p /tmp/dagger-cgroup-fix
  if [ -s /etc/dagger/engine.toml ]; then
    cp /etc/dagger/engine.toml /tmp/dagger-cgroup-fix/engine.toml
  else
    : > /tmp/dagger-cgroup-fix/engine.toml
  fi
  TOML=/tmp/dagger-cgroup-fix/engine.toml
  SETTING="  defaultCgroupParent = \"$ENGINE_CGROUP\""
  if grep -q '^\[worker\.oci\]' "$TOML"; then
    # Insert the setting on the line after [worker.oci]
    sed -i "/^\[worker\.oci\]/a\\
  $SETTING" "$TOML"
  else
    printf '\n[worker.oci]\n%s\n' "$SETTING" >> "$TOML"
  fi
  CFG=/tmp/dagger-cgroup-fix/engine.toml
else
  # cgroupns is in effect (or no cgroup v2). Fall back to the
  # standard dind setup that the upstream entrypoint does.
  if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    mkdir -p /sys/fs/cgroup/init
    xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
    sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
      > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
  fi
  CFG=/etc/dagger/engine.toml
fi
ulimit -n 1048576 || echo "cannot increase open FDs with ulimit, ignoring" >&2
# tini reaps the engine's orphaned children, as the upstream entrypoint does.
exec tini -- /usr/local/bin/dagger-engine --config "$CFG" "$@"
