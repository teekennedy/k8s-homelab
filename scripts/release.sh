#!/bin/sh
# Create releases for every project in the shared container manifest.
# Needs git-cliff, curl, jq, git, FORGEJO_TOKEN and Woodpecker CI_* variables.
# RELEASE_PLAN optionally names scripts/release-plan.sh's output: an artifact
# it lists as unchanged is not tagged.
# With GITHUB_TOKEN and GITHUB_REPOSITORY (owner/name), each release is also
# created on that GitHub repository.
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

github() {
  curl -sS -o "$response" -w '%{http_code}' \
    -H "Authorization: Bearer $GITHUB_TOKEN" \
    -H 'Accept: application/vnd.github+json' "$@"
}

# Forgejo's push mirror carries commits and tags to GitHub, but not releases.
github_release() {
  tag=$1
  if [ -z "${GITHUB_TOKEN:-}" ]; then
    return
  fi
  github_api="https://api.github.com/repos/$GITHUB_REPOSITORY"
  if [ "$(github "$github_api/releases/tags/$tag")" = 200 ]; then
    echo "GitHub release $tag already exists"
    return
  fi
  # GitHub creates the tag itself if the mirror has not pushed it yet, but
  # only at a commit it already has.
  attempts=0
  until [ "$(github "$github_api/commits/$CI_COMMIT_SHA")" = 200 ]; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 30 ]; then
      echo "$CI_COMMIT_SHA never reached $GITHUB_REPOSITORY" >&2
      exit 1
    fi
    sleep "${GITHUB_MIRROR_WAIT:-10}"
  done
  status=$(jq -n --arg tag "$tag" --arg sha "$CI_COMMIT_SHA" --arg body "$notes" \
    '{tag_name: $tag, target_commitish: $sha, name: $tag, body: $body}' |
    github -X POST "$github_api/releases" --data @-)
  if [ "$status" != 201 ]; then
    echo "Creating GitHub release $tag returned HTTP $status: $(cat "$response")" >&2
    exit 1
  fi
  echo "Created GitHub release $tag at $CI_COMMIT_SHA"
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
    github_release "$prefix-$version"
  done
done
