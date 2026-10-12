#!/bin/sh
# Emit reproducible OCI release metadata from a full-history tagged checkout.
set -eu

tag=${1:?release tag required}
root=$(git rev-parse --show-toplevel)
project=$(jq -ce --arg tag "$tag" '
  .[] | select(any(.artifacts[]; .prefix as $prefix |
    $tag | startswith($prefix + "-")))
' "$root/.dagger/releases.json")
directory=$(printf '%s' "$project" | jq -r .directory)
config=$(printf '%s' "$project" | jq -r .config)
canonical=$(printf '%s' "$project" | jq -r '.artifacts[0].prefix')
version=${tag##*-}
canonical_tag="$canonical-$version"
revision=$(git rev-parse "$tag^{commit}")
# --current operates on HEAD: verify that the requested tags refer to this checkout.
[ "$revision" = "$(git rev-parse HEAD)" ]
[ "$revision" = "$(git rev-parse "$canonical_tag^{commit}")" ]
notes=$(git cliff --workdir "$root/$directory" --repository "$root" \
  --config "$root/$directory/$config" --current --strip all)
created=$(git show -s --format=%cI "$revision")
jq -n --arg tag "$tag" --arg revision "$revision" --arg created "$created" \
  --arg changelog "$notes" \
  '{tag: $tag, revision: $revision, created: $created, changelog: $changelog}'
