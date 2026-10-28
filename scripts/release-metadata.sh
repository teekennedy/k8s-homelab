#!/bin/sh
# Emit reproducible OCI release metadata from a full-history tagged checkout.
set -eu

tag=${1:?release tag required}
root=$(git rev-parse --show-toplevel)
project=$(jq -ce --arg tag "$tag" '
  .[] | select(any(.artifacts[]; .prefix as $prefix |
    $tag | startswith($prefix + "-")))
' "$root/.dagger/modules/release/releases.json")
directory=$(printf '%s' "$project" | jq -r .directory)
config=$(printf '%s' "$project" | jq -r .config)
canonical=$(printf '%s' "$project" | jq -r '.artifacts[0].prefix')
version=${tag##*-}
canonical_tag="$canonical-$version"
revision=$(git rev-parse "$tag^{commit}")
# Verify that the requested tag refers to this checkout.
[ "$revision" = "$(git rev-parse HEAD)" ]
# Select the previous release from the same tags as release.sh, so the notes
# match the release's. Git selects it itself; --current can select another
# project's tag when several releases share a commit.
set -- git describe --tags --abbrev=0
for prefix in $(printf '%s' "$project" | jq -r '.artifacts[].prefix, .legacyPrefixes[]?'); do
  set -- "$@" --match "$prefix-[0-9]*"
done
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
