#!/bin/sh
# Create releases for every project in the shared container manifest.
# Needs git-cliff, curl, jq, git, FORGEJO_TOKEN and Woodpecker CI_* variables.
# RELEASE_PLAN optionally names scripts/release-plan.sh's output: an artifact
# it lists as unchanged is not tagged.
set -eu

root=$(git rev-parse --show-toplevel)
manifest="$root/.dagger/modules/release/releases.json"
api="$CI_FORGE_URL/api/v1/repos/$CI_REPO"
response=$(mktemp)
context=$(mktemp)
trap 'rm -f "$response" "$context"' EXIT

create_release() {
  tag=$1
  # Retries may encounter a release created before another artifact failed.
  if git rev-parse --verify "refs/tags/$tag" >/dev/null 2>&1; then
    if [ "$(git rev-parse "$tag^{commit}")" != "$CI_COMMIT_SHA" ]; then
      echo "Refusing to move existing tag $tag" >&2
      exit 1
    fi
    echo "Release $tag already exists at $CI_COMMIT_SHA"
    return
  fi
  status=$(jq -n --arg tag "$tag" --arg sha "$CI_COMMIT_SHA" --arg body "$notes" \
    '{tag_name: $tag, target_commitish: $sha, name: $tag, body: $body}' |
    curl -sS -o "$response" -w '%{http_code}' -X POST "$api/releases" \
      -H "Authorization: token $FORGEJO_TOKEN" \
      -H 'Content-Type: application/json' --data @-)
  if [ "$status" != 201 ]; then
    echo "Creating $tag returned HTTP $status: $(cat "$response")" >&2
    exit 1
  fi
  echo "Created $tag at $CI_COMMIT_SHA"
}

for project in $(jq -r 'keys[]' "$manifest"); do
  directory=$(jq -r --arg project "$project" '.[$project].directory' "$manifest")
  config=$(jq -r --arg project "$project" '.[$project].config' "$manifest")
  prefixes=$(jq -r --arg project "$project" '.[$project].artifacts[].prefix' "$manifest")
  canonical=$(printf '%s\n' "$prefixes" | head -n 1)
  # Use the same repository and project-specific config from its working directory.
  cliff() {
    git cliff --workdir "$root/$directory" --repository "$root" --config "$root/$directory/$config" "$@"
  }
  current=$(for prefix in $prefixes; do
    git tag --points-at "$CI_COMMIT_SHA" --list "$prefix-[0-9]*"
  done | head -n 1)
  if [ -n "$current" ]; then
    version=${current##*-}
    metadata=$("$root/scripts/release-metadata.sh" "$current")
    notes=$(printf '%s' "$metadata" | jq -r .changelog)
  else
    # Path filtering can drop a tagged release with no project changes. Git
    # supplies the baseline; cliff supplies only the commits after that tag.
    # Any artifact's tag marks an earlier release, as a release can tag only
    # some of them.
    set -- git describe --tags --abbrev=0
    for prefix in $prefixes; do
      set -- "$@" --match "$prefix-[0-9]*"
    done
    previous=$("$@" "$CI_COMMIT_SHA" 2>/dev/null || true)
    range=$CI_COMMIT_SHA
    if [ -n "$previous" ]; then
      range="$(git rev-parse "$previous^{commit}")..$CI_COMMIT_SHA"
    fi
    cliff --context "$range" | jq --arg previous "$previous" '
      [.[] | select(.version == null) |
        .previous = (if $previous == "" then null
          else (. | .version = $previous | .commits = [] | .previous = null) end)]
    ' > "$context"
    pending=$(jq '[.[].commits[]] | length' "$context")
    if [ "$pending" -eq 0 ]; then
      echo "No releasable $project changes."
      continue
    fi
    bumped=$(cliff --from-context "$context" --bumped-version)
    # Every artifact of the project shares the version stream.
    version=${bumped##*-}
    jq --arg tag "$canonical-$version" '.[0].version = $tag' "$context" > "$response"
    notes=$(cliff --from-context "$response" --strip all)
  fi
  for prefix in $prefixes; do
    unchanged=$(jq -r --arg prefix "$prefix" \
      '.[$prefix] | select(.changed == false) | .reason' "${RELEASE_PLAN:-/dev/null}")
    if [ -n "$unchanged" ]; then
      echo "Skipping $prefix-$version: $unchanged"
      continue
    fi
    create_release "$prefix-$version"
  done
done
