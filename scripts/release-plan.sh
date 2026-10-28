#!/bin/sh
# Write release.sh's RELEASE_PLAN to $1: whether each Nix-built artifact would
# publish a different image than its latest release (`dagger api call nix
# devenv release-change`). When devenv-ci's would not, also refresh
# devenv-ci.json's file hashes for its latest release, for
# scripts/record-devenv-ci.sh to commit, so checks keep pulling that image
# instead of building the same one.
# Needs git, jq and the dagger CLI, in a full-history checkout with tags.
set -eu

out=${1:?output path required}
root=$(git rev-parse --show-toplevel)
manifest="$root/.dagger/modules/release/releases.json"
record="$root/.dagger/modules/nix/devenv-ci.json"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/results"

# Each artifact's latest release reachable from this commit.
latest() {
  git tag --merged HEAD --list "$1-[0-9]*" --sort=-v:refname | head -n 1
}

# One comparison per artifact, in parallel: each is a dagger session of its
# own.
jq -r '.[].artifacts[] | select(.profiles) | "\(.prefix) \(.profiles | join(","))"' "$manifest" >"$work/artifacts"
pids=""
while read -r prefix profiles; do
  tag=$(latest "$prefix")
  [ -n "$tag" ] || continue
  # Moved into results only once complete, so a failed call leaves nothing.
  dagger api call nix devenv release-change --tag="$tag" --profiles="$profiles" </dev/null >"$work/$prefix" &&
    mv "$work/$prefix" "$work/results/$prefix" &
  pids="$pids $!"
done <"$work/artifacts"
for pid in $pids; do
  # A failed comparison leaves its artifact out of the plan, which releases it.
  wait "$pid" || echo "warning: an image comparison failed; its artifact counts as changed" >&2
done

for result in "$work"/results/*; do
  [ -f "$result" ] || continue
  jq --arg prefix "$(basename "$result")" '{($prefix): .}' "$result"
done | jq -s 'add // {}' >"$out"
cat "$out"

if [ "$(jq '.["devenv-ci"].changed' "$out")" = false ]; then
  dagger api call nix devenv ci-release --tag="$(latest devenv-ci)" export --path="$record"
fi
