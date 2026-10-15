#!/bin/sh
# Emit reproducible OCI release metadata from a full-history tagged checkout.
set -eu

tag=${1:?release tag required}
root=$(git rev-parse --show-toplevel)
project=$(jq -ce --arg tag "$tag" '
  .[] | select(any(.artifacts[]; .prefix as $prefix |
    $tag | startswith($prefix + "-")))
' "$root/.dagger/modules/homelab/releases.json")
directory=$(printf '%s' "$project" | jq -r .directory)
config=$(printf '%s' "$project" | jq -r .config)
canonical=$(printf '%s' "$project" | jq -r '.artifacts[0].prefix')
version=${tag##*-}
canonical_tag="$canonical-$version"
revision=$(git rev-parse "$tag^{commit}")
# Verify that the requested tags refer to this checkout.
[ "$revision" = "$(git rev-parse HEAD)" ]
[ "$revision" = "$(git rev-parse "$canonical_tag^{commit}")" ]
# Select the previous project tag from Git itself. --current can select another
# project's tag when several releases share a commit.
set -- git describe --tags --abbrev=0 --match "$canonical-[0-9]*"
previous=$("$@" "$revision^" 2>/dev/null || true)
range=$revision
if [ -n "$previous" ]; then
  range="$(git rev-parse "$previous^{commit}")..$revision"
fi
notes=$(git cliff --workdir "$root/$directory" --repository "$root" \
  --config "$root/$directory/$config" --tag "$canonical_tag" --strip all "$range")
created=$(git show -s --format=%cI "$revision")
jq -n --arg tag "$tag" --arg revision "$revision" --arg created "$created" \
  --arg changelog "$notes" \
  '{tag: $tag, revision: $revision, created: $created, changelog: $changelog}'
