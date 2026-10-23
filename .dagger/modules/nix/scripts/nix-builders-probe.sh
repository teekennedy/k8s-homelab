#!/bin/sh
# Probes every candidate Nix builder in parallel, by asking its daemon for its
# store info over ssh. Prints one line per machine:
#
#   trusted|untrusted|unknown <machine>     the daemon answered
#   unreachable <machine>: <reason>         it did not
#
# Always exits 0: a builder that does not answer is an answer, not an error.
set -u

work=$(mktemp -d)
i=0
while IFS= read -r machine; do
  [ -n "$machine" ] || continue
  i=$((i + 1))
  (
    uri=${machine%% *}
    if info=$(timeout 20 nix --extra-experimental-features nix-command \
      store info --json --store "$uri" 2>"$work/$i.err"); then
      case $info in
      *'"trusted":true'*) status=trusted ;;
      *'"trusted":false'*) status=untrusted ;;
      *) status=unknown ;;
      esac
      echo "$status $machine" >"$work/$i.out"
    else
      reason=$(grep -v '^Warning: Identity file' "$work/$i.err" | tail -n 2 | tr '\n' ' ')
      echo "unreachable $machine: ${reason:-no answer within 20s}" >"$work/$i.out"
    fi
  ) &
done <"${NIX_BUILDERS_CANDIDATES:-/etc/homelab-nix/candidates}"
wait

for out in "$work"/*.out; do
  [ -e "$out" ] && cat "$out"
done
exit 0
