#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage: create-pr.sh [OPTIONS] [BRANCH]

Push a feature branch to origin (Forgejo) and open a PR against main, or update
the branch's open PR if there is one. Prints the PR URL on the last line.

Arguments:
  BRANCH                     Branch to push/PR (default: current branch)

Options:
  -t, --title TITLE          PR title (default: the commit subject when the branch
                             has one commit, otherwise the oldest commit's subject)
  -b, --body BODY            PR description (default: the commit body, or a list of
                             commit subjects when the branch has several commits)
  -F, --body-file FILE       Read the PR description from FILE ('-' for stdin)
  -B, --base BRANCH          Branch to open the PR against (default: main)
  -d, --draft                Mark the PR as a draft (prefixes the title with 'WIP: ')
      --auto-merge           Schedule auto-merge once checks pass. Needs merge rights
                             on the base branch, which bot accounts don't have.
      --no-auto-merge        Cancel auto-merge if it is already scheduled. Not needed
                             otherwise: auto-merge is off unless --auto-merge is given.
  -M, --merge-method METHOD  With --auto-merge: merge, rebase, rebase-merge, squash,
                             fast-forward-only (default: the repo's default method)
  -f, --force                Force-push (with lease) if the remote branch has diverged
  -h, --help                 Show this help message

On an existing PR, --title and --body/--body-file replace the current ones;
omitting them leaves the PR's text alone.

Authentication: FORGEJO_TOKEN if set (it is inside an agent sandbox), otherwise
the token fj stores for the host (~/Library/Application Support/forgejo-cli.forgejo-cli/keys.json).
Pushing uses whatever git credentials the environment already has.

Examples:
  create-pr.sh -t "fix(foo): handle empty input" -F /tmp/pr-body.md
  create-pr.sh --draft -t "feat(bar): wip" -b "Not ready for review."
  create-pr.sh --auto-merge -M squash
EOF
}

base_branch="main"
branch=""
title=""
body=""
body_set=0
draft=0
# Unset leaves a PR's scheduled auto-merge alone; 1 schedules it, 0 cancels it.
auto_merge=""
merge_method=""
force_push=0

need_value() {
  if [[ $# -lt 2 ]]; then
    echo "error: $1 needs a value" >&2
    exit 1
  fi
}

while [[ $# -gt 0 ]]; do
  case $1 in
  -h | --help)
    usage
    exit 0
    ;;
  -t | --title)
    need_value "$@"
    title=$2
    shift 2
    ;;
  -b | --body)
    need_value "$@"
    body=$2
    body_set=1
    shift 2
    ;;
  -F | --body-file)
    need_value "$@"
    if [[ $2 == "-" ]]; then
      body=$(cat)
    elif [[ -f $2 ]]; then
      body=$(cat "$2")
    else
      echo "error: body file '$2' not found" >&2
      exit 1
    fi
    body_set=1
    shift 2
    ;;
  -B | --base)
    need_value "$@"
    base_branch=$2
    shift 2
    ;;
  -d | --draft)
    draft=1
    shift
    ;;
  --auto-merge)
    auto_merge=1
    shift
    ;;
  --no-auto-merge)
    auto_merge=0
    shift
    ;;
  -M | --merge-method)
    need_value "$@"
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

find_open_pr() {
  api_call GET "/repos/${owner}/${repo}/pulls?state=open&limit=50" |
    jq -r --arg branch "$branch" '[.[] | select(.head.ref == $branch)][0].number // empty'
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

if [[ $draft -eq 1 && -n $title ]]; then
  title="WIP: ${title#WIP: }"
fi

echo "Looking for an existing open PR for ${branch}..."
pr_number=$(find_open_pr)

if [[ -z $pr_number ]]; then
  # Fill what wasn't given from the commits the PR will carry.
  if [[ -z $title || $body_set -eq 0 ]]; then
    git fetch origin "$base_branch" --quiet || true
    range="origin/${base_branch}..${branch}"
    if ! git rev-parse --verify --quiet "origin/${base_branch}" >/dev/null; then
      range="${base_branch}..${branch}"
    fi
    subjects=$(git log --reverse --format=%s "$range")
    if [[ -z $subjects ]]; then
      echo "error: ${branch} has no commits ahead of ${base_branch}" >&2
      exit 1
    fi
    if [[ -z $title ]]; then
      title=$(head -n1 <<<"$subjects")
      if [[ $draft -eq 1 ]]; then
        title="WIP: ${title#WIP: }"
      fi
    fi
    if [[ $body_set -eq 0 ]]; then
      if [[ $(wc -l <<<"$subjects") -eq 1 ]]; then
        body=$(git log --format=%b "$range")
      else
        body=$(awk '{print "- " $0}' <<<"$subjects")
      fi
    fi
  fi

  echo "Creating PR: ${branch} -> ${base_branch}..."
  create_payload=$(jq -n --arg title "$title" --arg body "$body" \
    --arg head "$branch" --arg base "$base_branch" \
    '{title: $title, body: $body, head: $head, base: $base}')
  api_call POST "/repos/${owner}/${repo}/pulls" -d "$create_payload" >/dev/null
  pr_number=$(find_open_pr)
  if [[ -z $pr_number ]]; then
    echo "error: PR creation appeared to succeed but could not find the resulting PR" >&2
    exit 1
  fi
else
  echo "Found existing PR #${pr_number}, updating it."
  if [[ $draft -eq 1 && -z $title ]]; then
    title=$(api_call GET "/repos/${owner}/${repo}/pulls/${pr_number}" | jq -r .title)
    title="WIP: ${title#WIP: }"
  fi
  if [[ -n $title || $body_set -eq 1 ]]; then
    edit_payload=$(jq -n --arg title "$title" --arg body "$body" \
      --argjson set_body "$body_set" \
      '(if $title != "" then {title: $title} else {} end)
       + (if $set_body == 1 then {body: $body} else {} end)')
    api_call PATCH "/repos/${owner}/${repo}/pulls/${pr_number}" -d "$edit_payload" >/dev/null
  fi
fi

if [[ $auto_merge == 1 ]]; then
  repo_info=$(api_call GET "/repos/${owner}/${repo}")
  if [[ -z $merge_method ]]; then
    merge_method=$(jq -r '.default_merge_style // "rebase"' <<<"$repo_info")
  fi
  delete_branch=$(jq -r '.default_delete_branch_after_merge // true' <<<"$repo_info")

  echo "Enabling auto-merge (method: ${merge_method}, delete branch on merge: ${delete_branch})..."
  merge_payload=$(jq -n --arg style "$merge_method" --argjson delete "$delete_branch" \
    '{Do: $style, merge_when_checks_succeed: true, delete_branch_after_merge: $delete}')
  # Forgejo answers 405 when asked right after a push, before it has finished
  # evaluating the new head; the same request succeeds moments later.
  if ! merge_out=$(api_call POST "/repos/${owner}/${repo}/pulls/${pr_number}/merge" -d "$merge_payload" 2>&1); then
    echo "Auto-merge request failed, retrying in 5s: ${merge_out}" >&2
    sleep 5
    if ! merge_out=$(api_call POST "/repos/${owner}/${repo}/pulls/${pr_number}/merge" -d "$merge_payload" 2>&1); then
      echo "error: could not enable auto-merge: ${merge_out}" >&2
      exit 1
    fi
  fi
elif [[ $auto_merge == 0 ]]; then
  echo "Cancelling any scheduled auto-merge (--no-auto-merge)..."
  api_call DELETE "/repos/${owner}/${repo}/pulls/${pr_number}/merge" >/dev/null 2>&1 || true
fi

echo "https://${forge_host}/${owner}/${repo}/pulls/${pr_number}"
