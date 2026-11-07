---
name: create-pr
description: >-
  Push the current branch and open (or update) a pull request on ops/k8s-homelab
  with scripts/create-pr.sh. Use whenever you have finished changes in this
  repository and need to publish them for review, set a PR title or description,
  or open a draft.
---

# create-pr — publish a branch as a pull request

`scripts/create-pr.sh` in the repository root pushes a branch to Forgejo and
opens a PR against `main`, or updates the branch's open PR if there already is
one. It needs nothing beyond what the sandbox already has: `FORGEJO_TOKEN` for
the API and the git credential helper for the push. Do not use `fj`, `gh` or raw
`curl` to open the PR.

## Do this

1. Commit your work on a feature branch (never on `main`), with Conventional
   Commit messages that include a scope, e.g. `fix(openhands): correct probe path`.
2. Write the PR description to a file, then run:

   ```bash
   scripts/create-pr.sh -t "fix(openhands): correct probe path" -F /tmp/pr-body.md
   ```

3. The last line of output is the PR URL. Report it.

Auto-merge is off by default. Your account cannot merge to `main`; a human
reviews and merges. Do not pass `--auto-merge`.

## What the title and description should say

- **Title:** the Conventional Commit subject of the change, under ~70 characters.
- **Description:** what changed and why, anything a reviewer should check, and
  anything you could not verify. Say plainly if a check failed or was skipped.

Omit `-t` and `-b` and the script fills them from the branch's commits: the
subject and body of a single commit, or the oldest subject plus a list of all
subjects for several. That is a fallback; write the description yourself.

## Options

| Option | Meaning |
| --- | --- |
| `-t, --title TITLE` | PR title |
| `-b, --body BODY` | PR description, inline |
| `-F, --body-file FILE` | PR description from a file (`-` reads stdin); use this for anything multi-line |
| `-B, --base BRANCH` | Base branch (default `main`) |
| `-d, --draft` | Open as a draft (prefixes the title with `WIP: `) |
| `-f, --force` | Force-push with lease if the remote branch has diverged |
| `[BRANCH]` | Branch to publish (default: the current branch) |

`--auto-merge`, `--no-auto-merge` and `-M/--merge-method` exist for a human
running the script from a laptop; ignore them.

## Updating an existing PR

Run the script again on the same branch after pushing more commits. It reuses
the open PR rather than creating a second one. `-t` and `-b`/`-F` replace the
PR's current title and description; leave them off to keep the text as it is.

## Failure modes

- `origin/<branch> has diverged` — someone changed the remote branch. Look at
  what is there (`git log origin/<branch>`), integrate it with `git pull --rebase`,
  and only use `--force` if you are sure the remote's commits are disposable.
- `no commits ahead of main` — nothing to open a PR for; check you committed.
- `HTTP 401/403` from the API — the token is missing or lacks scope. Stop and
  report it; there is nothing to fix in the branch.

After the PR exists, use the `forgejo-iterate` skill to drive its CI checks to
green.
