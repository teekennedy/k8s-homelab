#!/bin/sh
# Write release.sh's RELEASE_PLAN to $1: whether each Nix-built artifact would
# publish a different image than its latest release (`dagger api call release
# plan`). When devenv-ci's would not, also refresh devenv-ci.json's file hashes
# for its latest release, for scripts/record-devenv-ci.sh to commit, so checks
# keep pulling that image instead of building the same one.
# Needs git, jq and the dagger CLI, in a full-history checkout with tags. Runs
# dagger with the ci env, like the PR checks, whose ci toolchain build the
# release module's construction then finds in the engine's cache; source
# scripts/nix-ci-ssh-agent.sh first.
set -eu

out=${1:?output path required}
root=$(git rev-parse --show-toplevel)
manifest="$root/.dagger/modules/release/releases.json"
record="$root/.dagger/modules/nix/devenv-ci.json"

# Each artifact's latest release reachable from this commit.
latest() {
  git tag --merged HEAD --list "$1-[0-9]*" --sort=-v:refname | head -n 1
}

published=""
for prefix in $(jq -r '.[].artifacts[] | select(.profiles) | .prefix' "$manifest"); do
  tag=$(latest "$prefix")
  if [ -n "$tag" ]; then
    published="$published${published:+,}$tag"
  fi
done
if [ -z "$published" ]; then
  echo '{}' >"$out"
  exit 0
fi

dagger --env ci api call release plan --published="$published" export --path="$out"
cat "$out"

if [ "$(jq '.["devenv-ci"].changed' "$out")" = false ]; then
  dagger --env ci api call nix devenv ci-release --tag="$(latest devenv-ci)" export --path="$record"
fi
