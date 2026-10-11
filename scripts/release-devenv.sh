#!/bin/sh
# Tags a new devenv release when conventional commits touching the devenv image
# inputs (see cliff.toml) have landed since the last devenv-* tag. Creating the
# Forgejo release creates the tag, which triggers .woodpecker/devenv-release.yaml
# to publish the image.
#
# Needs git-cliff, curl, jq and git in a full-history clone, plus
# FORGEJO_TOKEN and Woodpecker's CI_* variables.
set -euo pipefail

# Versioning starts at devenv-1.0.0 on this commit. It is a baseline only: the
# commit predates the publish pipeline, so no image is built for it.
BOOTSTRAP_SHA=2d9a45421e0efab88173c22bc49217092927041e

api="$CI_FORGE_URL/api/v1/repos/$CI_REPO"

create_release() { # tag, commit, notes
  jq -n --arg tag "$1" --arg sha "$2" --arg body "$3" \
    '{tag_name: $tag, target_commitish: $sha, name: $tag, body: $body}' |
    curl --fail-with-body -sS -X POST "$api/releases" \
      -H "Authorization: token $FORGEJO_TOKEN" \
      -H 'Content-Type: application/json' \
      --data @- >/dev/null
  echo "Created $1 at $2"
}

if [ -z "$(git tag --list 'devenv-*')" ]; then
  create_release devenv-1.0.0 "$BOOTSTRAP_SHA" 'Baseline for devenv versioning.'
  # The release created the tag on the server; mirror it locally for git-cliff.
  git tag devenv-1.0.0 "$BOOTSTRAP_SHA"
fi

pending=$(git cliff --unreleased --context | jq '[.[] | select(.version == null) | .commits[]] | length')
if [ "$pending" -eq 0 ]; then
  echo 'No releasable devenv changes.'
  exit 0
fi

version=$(git cliff --bumped-version)
create_release "$version" "$CI_COMMIT_SHA" "$(git cliff --unreleased --tag "$version" --strip all)"
