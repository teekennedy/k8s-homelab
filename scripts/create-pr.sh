#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "Usage: $0 [OPTIONS] [BRANCH]"
  echo ""
  echo "Push a feature branch to origin (Forgejo) and open/update a PR against main,"
  echo "with auto-merge and delete-branch-on-merge configured."
  echo ""
  echo "Arguments:"
  echo "  BRANCH                    Branch to push/PR (default: current branch)"
  echo ""
  echo "Options:"
  echo "  --no-auto-merge            Don't enable auto-merge (cancels it if already scheduled)"
  echo "  -M, --merge-method METHOD  Merge method: merge, rebase, rebase-merge, squash,"
  echo "                             fast-forward-only (default: repo's default merge method)"
  echo "  -f, --force                Force-push (with lease) if the remote branch has diverged"
  echo "  -h, --help                 Show this help message"
  echo ""
  echo "Needs a Forgejo API token (fj has no way to schedule auto-merge itself). Uses"
  echo "FORGEJO_TOKEN if set, otherwise falls back to fj's own stored token for the host"
  echo "(~/Library/Application Support/forgejo-cli.forgejo-cli/keys.json)."
}

base_branch="main"
branch=""
auto_merge=1
merge_method=""
force_push=0

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    usage
    exit 0
    ;;
  --no-auto-merge)
    auto_merge=0
    shift
    ;;
  -M | --merge-method)
    merge_method=$2
    shift 2
    ;;
  -f | --force)
    force_push=1
    shift
    ;;
  -*)
    echo "Unknown option: $1" >&2
    usage >&2
    exit 1
    ;;
  *)
    if [[ -n $branch ]]; then
      echo "Only one branch may be specified (got '$branch' and '$1')" >&2
      exit 1
    fi
    branch=$1
    shift
    ;;
  esac
done

if [[ -z $branch ]]; then
  branch=$(git branch --show-current)
fi

if [[ -z $branch ]]; then
  echo "error: not currently on a branch (detached HEAD) and no branch specified" >&2
  exit 1
fi

if [[ $branch == "$base_branch" ]]; then
  echo "error: no feature branch specified (currently on '$base_branch')" >&2
  echo "Pass a branch name, or check out a feature branch first." >&2
  exit 1
fi

if ! git rev-parse --verify --quiet "refs/heads/$branch" >/dev/null; then
  echo "error: local branch '$branch' does not exist" >&2
  exit 1
fi

if [[ -n $merge_method ]]; then
  case $merge_method in
  merge | rebase | rebase-merge | squash | fast-forward-only) ;;
  *)
    echo "error: unknown merge method '$merge_method' (want: merge, rebase, rebase-merge, squash, fast-forward-only)" >&2
    exit 1
    ;;
  esac
fi

remote_url=$(git remote get-url origin)
# https://git.msng.to/ops/k8s-homelab.git -> host=git.msng.to owner=ops repo=k8s-homelab
if [[ $remote_url =~ ^https?://([^/]+)/([^/]+)/([^/]+)$ ]]; then
  forge_host=${BASH_REMATCH[1]}
  owner=${BASH_REMATCH[2]}
  repo=${BASH_REMATCH[3]%.git}
else
  echo "error: could not parse owner/repo from origin remote '$remote_url'" >&2
  exit 1
fi

fj_keys_file="${HOME}/Library/Application Support/forgejo-cli.forgejo-cli/keys.json"

forgejo_token=${FORGEJO_TOKEN:-}
if [[ -z $forgejo_token && -f $fj_keys_file ]]; then
  forgejo_token=$(jq -r --arg host "$forge_host" '.hosts[$host].token // empty' "$fj_keys_file")
fi

if [[ -z $forgejo_token ]]; then
  echo "error: no token available for ${forge_host}." >&2
  echo "Either export FORGEJO_TOKEN, or run 'fj auth login' / 'fj auth add-token' for this host." >&2
  exit 1
fi

api="https://${forge_host}/api/v1"

api_call() {
  local method=$1 path=$2
  shift 2
  curl -sS --fail-with-body -X "$method" \
    -H "Authorization: token ${forgejo_token}" \
    -H "Content-Type: application/json" \
    "${api}${path}" "$@"
}

echo "Fetching origin/${branch}..."
git fetch origin "$branch" --quiet || true

if git rev-parse --verify --quiet "refs/remotes/origin/${branch}" >/dev/null; then
  if ! git merge-base --is-ancestor "origin/${branch}" "$branch"; then
    if [[ $force_push -eq 1 ]]; then
      echo "Remote branch has diverged from local; force-pushing (--force given)..."
      git push --force-with-lease origin "${branch}:${branch}"
    else
      echo "error: origin/${branch} has diverged from local '${branch}' (e.g. rebased via the Forgejo UI)." >&2
      echo "Re-sync locally (git pull --rebase / git reset --hard origin/${branch}) or pass --force to overwrite the remote." >&2
      exit 1
    fi
  else
    git push origin "${branch}:${branch}"
  fi
else
  git push -u origin "${branch}:${branch}"
fi

echo "Looking for an existing open PR for ${branch}..."
pr_number=$(api_call GET "/repos/${owner}/${repo}/pulls?state=open&limit=50" |
  jq -r --arg branch "$branch" '[.[] | select(.head.ref == $branch)][0].number // empty')

if [[ -z $pr_number ]]; then
  echo "Creating PR: ${branch} -> ${base_branch}..."
  fj pr create --autofill --base "$base_branch" --head "$branch" --repo "${owner}/${repo}" >/dev/null
  pr_number=$(api_call GET "/repos/${owner}/${repo}/pulls?state=open&limit=50" |
    jq -r --arg branch "$branch" '[.[] | select(.head.ref == $branch)][0].number // empty')
  if [[ -z $pr_number ]]; then
    echo "error: PR creation appeared to succeed but could not find the resulting PR" >&2
    exit 1
  fi
else
  echo "Found existing PR #${pr_number}, updating it."
fi

repo_info=$(api_call GET "/repos/${owner}/${repo}")

if [[ $auto_merge -eq 1 ]]; then
  if [[ -z $merge_method ]]; then
    merge_method=$(jq -r '.default_merge_style // "rebase"' <<<"$repo_info")
  fi
  delete_branch=$(jq -r '.default_delete_branch_after_merge // true' <<<"$repo_info")

  echo "Enabling auto-merge (method: ${merge_method}, delete branch on merge: ${delete_branch})..."
  api_call POST "/repos/${owner}/${repo}/pulls/${pr_number}/merge" \
    -d "$(jq -n --arg do "$merge_method" --argjson delete "$delete_branch" \
      '{Do: $do, merge_when_checks_succeed: true, delete_branch_after_merge: $delete}')" >/dev/null
else
  echo "Auto-merge disabled (--no-auto-merge); cancelling any scheduled auto-merge..."
  api_call DELETE "/repos/${owner}/${repo}/pulls/${pr_number}/merge" >/dev/null 2>&1 || true
fi

echo "https://${forge_host}/${owner}/${repo}/pulls/${pr_number}"
