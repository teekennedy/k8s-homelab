#!/bin/sh
# Commit the devenv-ci release record to main. Run from the release tag's
# checkout, after `dagger api call nix devenv ci-release` wrote the record there.
# Needs git, CI_COMMIT_TAG, and FORGEJO_TOKEN with Woodpecker's CI_FORGE_URL
# and CI_REPO, or RELEASE_REMOTE to push somewhere else.
set -eu

record=.dagger/modules/nix/devenv-ci.json
tag=${CI_COMMIT_TAG:?release tag required}
remote=${RELEASE_REMOTE:-"https://release-bot:$FORGEJO_TOKEN@${CI_FORGE_URL#*://}/$CI_REPO.git"}
saved=$(mktemp)
trap 'rm -f "$saved"' EXIT
cp "$record" "$saved"

# Prints the version of the release the record names, if any.
recorded_version() {
  [ -f "$record" ] || return 0
  while IFS= read -r line; do
    case $line in
      *'"tag": "devenv-ci-'*)
        line=${line#*'"tag": "devenv-ci-'}
        echo "${line%%\"*}"
        return
        ;;
    esac
  done <"$record"
}

# Succeeds when version $1 is newer than $2, both x.y.z.
newer() {
  saved_ifs=$IFS
  IFS=.
  # shellcheck disable=SC2086
  set -- $1 $2
  IFS=$saved_ifs
  if [ "$1" -ne "$4" ]; then [ "$1" -gt "$4" ]; return; fi
  if [ "$2" -ne "$5" ]; then [ "$2" -gt "$5" ]; return; fi
  [ "$3" -gt "$6" ]
}

git config user.name release-bot
git config user.email release-bot@msng.to

# Another push can land on main between the fetch and the push; retry on top.
for attempt in 1 2 3; do
  git fetch --quiet "$remote" main
  git checkout --quiet --force -B main FETCH_HEAD
  recorded=$(recorded_version)
  # A retried pipeline for an older release must not replace a newer one.
  if [ -n "$recorded" ] && newer "$recorded" "${tag#devenv-ci-}"; then
    echo "main already records the newer devenv-ci-$recorded"
    exit 0
  fi
  cp "$saved" "$record"
  if git diff --quiet -- "$record" && git ls-files --error-unmatch "$record" >/dev/null 2>&1; then
    echo "main already records $tag"
    exit 0
  fi
  git add "$record"
  git commit --quiet -m "chore(release): Record $tag image"
  if git push --quiet "$remote" HEAD:main; then
    echo "Recorded $tag on main"
    exit 0
  fi
  echo "Pushing to main failed (attempt $attempt)" >&2
done
exit 1
