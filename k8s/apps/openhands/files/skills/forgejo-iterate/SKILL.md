---
name: forgejo-iterate
description: >-
  Drive a Forgejo pull request to green. Poll the combined commit status, pull
  failing step logs out of Woodpecker, diagnose, fix, push, and repeat until the
  PR is mergeable or a blocker needs a human. Use for any request to iterate on,
  babysit, verify or un-red a PR on a Forgejo host with Woodpecker CI.
---

# forgejo-iterate — drive a PR to green

You are the loop. There is no script to run: you poll, diagnose, fix, push, and
poll again. The loop ends when CI is green on the PR's current head commit, or
when something needs a human.

Everything here is `curl` + `jq` against two HTTP APIs. Nothing in it is
specific to the tool that started the session.

## What must be set

| Variable | Used for |
| --- | --- |
| `FORGEJO_URL`, `FORGEJO_OWNER`, `FORGEJO_REPO`, `FORGEJO_TOKEN` | everything |
| `WOODPECKER_URL`, `WOODPECKER_TOKEN` | reading failing step logs, restarting a flaky pipeline |

If the Woodpecker pair is missing you can still run the loop — you just cannot
read logs or restart, so a red check becomes "blocked, needs a human" instead of
something you can diagnose. Say so rather than guessing at the cause.

Define these once at the start of the session:

```bash
fj() {  # fj <METHOD> <path under /api/v1> [json body]
  local method="$1" path="$2" body="${3:-}"
  curl -sS --fail-with-body --max-time 60 -X "$method" \
    -H "Authorization: token $FORGEJO_TOKEN" \
    -H 'Accept: application/json' \
    ${body:+-H 'Content-Type: application/json' --data-binary "$body"} \
    "${FORGEJO_URL%/}/api/v1$path"
}
R="/repos/$FORGEJO_OWNER/$FORGEJO_REPO"

wp() {  # wp <METHOD> <path under /api>
  curl -sS --fail-with-body --max-time 120 -X "$1" \
    -H "Authorization: Bearer $WOODPECKER_TOKEN" \
    -H 'Accept: application/json' \
    "${WOODPECKER_URL%/}/api$2"
}
```

## The loop

1. Push the branch and make sure a PR exists.
2. Read the PR's current head SHA.
3. Poll the combined commit status for that SHA.
4. Poll review feedback.
5. Decide: green, fix, restart, or wait.
6. On a fix: edit, commit, push, go to 2 — **the new SHA has not been checked yet.**

Pushing a fix is one iteration, not the end. Keep going until the checks are
green on the SHA that is currently at the head of the branch.

Do not stop to ask whether to keep polling. Continue until a stop condition
below is met, or the user interrupts.

## Step 1 — push, and ensure a PR exists

```bash
git push origin HEAD
BRANCH=$(git rev-parse --abbrev-ref HEAD)
```

Reuse an open PR for this branch rather than creating a second one — Forgejo
rejects the duplicate with a 409 whose message explains nothing:

```bash
PR=$(fj GET "$R/pulls?state=open&limit=50" \
  | jq -r --arg b "$BRANCH" 'map(select(.head.ref == $b)) | .[0].number // empty')

if [ -z "$PR" ]; then
  PR=$(fj POST "$R/pulls" "$(jq -n \
    --arg t "<one-line title>" --arg b "<body>" \
    --arg head "$BRANCH" --arg base "main" \
    '{title:$t, body:$b, head:$head, base:$base}')" | jq -r .number)
fi
echo "PR #$PR"
```

Forgejo has no "convert to draft" API call: a draft is a PR whose title carries
a work-in-progress prefix (`WIP:` by default), and you clear it with a normal
`PATCH $R/pulls/$PR` on the title. Only bother if the user asks for a draft.

## Step 2 — read the PR state

```bash
fj GET "$R/pulls/$PR" | jq '{
  sha: .head.sha, state, merged, mergeable, draft, title,
  base: .base.ref, url: .html_url
}'
```

- `state` is `closed` or `merged` is true → stop immediately.
- `mergeable: false` → there is a conflict with the base branch. Rebase or merge
  the base in, push, and go back to step 2.
- `.head.sha` is the SHA everything below is about. Re-read it after every push;
  never poll a status for a SHA you have moved past.

## Step 3 — poll the combined commit status

```bash
SHA=$(fj GET "$R/pulls/$PR" | jq -r .head.sha)
fj GET "$R/commits/$SHA/status" | jq '{
  state,
  checks: [.statuses[] | {context, status, url: .target_url, description}]
}'
```

`state` is the rollup: `success`, `pending`, `failure`, `error`, or `warning`.

- `success` → checks are green **for this SHA**. Go to step 4.
- `pending` → a pipeline is running or queued. Wait (see cadence) and re-poll.
- `failure` / `error` → go to step 3a.
- `total_count: 0` → no pipeline has reported yet. This is normal for the first
  seconds after a push; if it persists for several minutes, CI never started —
  that is a blocker for a human, not something to fix in the branch.

### Step 3a — read the failing logs out of Woodpecker

The status entry's `target_url` is the only place the pipeline identifiers
appear. It looks like `https://<host>/repos/<repo_id>/pipeline/<number>/<step>`:

```bash
URL=$(fj GET "$R/commits/$SHA/status" \
  | jq -r '[.statuses[] | select(.status=="failure" or .status=="error")][0].target_url')
REPO_ID=$(echo "$URL" | sed -E 's#.*/repos/([0-9]+)/pipeline/.*#\1#')
PIPE=$(echo    "$URL" | sed -E 's#.*/pipeline/([0-9]+).*#\1#')
```

Find the steps that actually failed, then download their logs as plain text:

```bash
wp GET "/repos/$REPO_ID/pipelines/$PIPE" | jq -r '
  [.workflows[]?.children[]?
   | select(.state=="failure" or .state=="error" or .state=="killed")]
  | .[] | "\(.id)\t\(.name)"'

# per failed step id:
curl -sS --fail-with-body -H "Authorization: Bearer $WOODPECKER_TOKEN" \
  "${WOODPECKER_URL%/}/api/repos/$REPO_ID/logs/$PIPE/<step_id>/download"
```

Use the `/download` endpoint — it returns the log as text. The plain
`/logs/<pipeline>/<step>` endpoint returns JSON entries whose `data` is
base64-encoded, which you would only have to decode back.

Build logs are long. Read the tail first; the error is almost always near the
end. Only page further back when the tail does not explain the failure.

## Step 4 — poll review feedback

```bash
fj GET "$R/pulls/$PR/reviews" | jq '
  [.[] | {id, state, user: .user.login, body: .body[0:300], submitted_at, stale}]
  | sort_by(.submitted_at)'
fj GET "$R/issues/$PR/comments" | jq '[.[] | {id, user: .user.login, body: .body[0:300]}]'
```

Inline comments belong to a review:

```bash
fj GET "$R/pulls/$PR/reviews/<review_id>/comments" \
  | jq '[.[] | {id, path, line: (.line // .original_line), body: .body[0:300]}]'
```

- `REQUEST_CHANGES` → read it, fix the code.
- `APPROVED` → nothing to do.
- `COMMENT` → may be actionable; read and decide.
- `stale: true` → the review is against an older SHA. Treat it as still
  actionable unless the code it points at is already what it asked for.

Check existing feedback on the first iteration too, not just comments that
arrive after you start watching. Reply to what you address:

```bash
fj POST "$R/issues/$PR/comments" "$(jq -n --arg b "Addressed in $(git rev-parse --short HEAD): <what changed>" '{body:$b}')"
```

Forgejo has no "resolve conversation" API, so a reply naming the commit is how a
thread gets closed out. Reply to every piece of feedback you acted on, and to
anything you deliberately did not act on, with one line saying why.

## Step 5 — decide

Priority order, highest first:

1. PR closed or merged → stop.
2. `mergeable: false` → resolve the conflict, push, restart at step 2.
3. Review requested changes → fix, push, restart at step 2. Do this *before*
   restarting flaky pipelines: the push retriggers CI anyway, so a restart on
   the old SHA is wasted.
4. Checks failed → classify (below), then fix or restart.
5. Anything pending → wait, re-poll.
6. All green on the current SHA and mergeable → done.

### Classifying a failure

**Fix the branch** when the log shows:

- a lint, format, type or schema error in a file the branch touched
- a deterministic test failure in the area the branch changed
- a rendering or validation failure from a manifest the branch edited
- a generator that reports a diff — run the generator and commit its output

**Restart the pipeline** when the log shows:

- a network, DNS, or registry timeout
- an agent that could not start, or a lost connection to it
- a failure in a component the branch did not touch, with no error attributable
  to the change
- a rate limit or a transient upstream outage

```bash
wp POST "/repos/$REPO_ID/pipelines/$PIPE"
```

At most **3 restarts per SHA**. After that treat the failure as real, even if it
still smells flaky, and say so in the final summary.

When it is genuinely ambiguous, read the log once more before choosing. One
careful read beats three restarts.

## Polling cadence

- Checks pending or failing: every 30–60s.
- Green, waiting on a human review: start at 60s and back off — 2m, 4m, 8m, 16m,
  32m — capped at an hour.
- Reset to 60s on any change: new SHA, status change, a new comment, mergeability
  flipping.
- Immediately after pushing a fix: re-poll at once, then settle back to 30–60s.

Report status changes, not every poll. A heartbeat every few polls during a long
quiet wait is enough.

## Stop conditions

Stop when:

- every check is green on the current head SHA and the PR is mergeable;
- the PR is merged or closed;
- the restart budget is spent and the failure is real;
- something needs a human — a permission problem, an outage, a conflict you
  cannot resolve safely, or a change to the intent of the PR.

**Not** stop conditions: you pushed a fix; you replied to a review; checks are
still running; checks are green but nobody has reviewed yet.

Running out of iterations is a **failure**, not a success. Say the PR is still
red and why. Never report a red PR as done.

## Git safety

- Work only on the PR's head branch.
- No force-push, no rebase of other people's commits, no branch deletion.
- Check for unrelated uncommitted changes before you start editing. If there are
  some, ask.
- Commit messages follow the repo's convention. Conventional Commits with a
  scope, e.g. `fix(openhands): correct probe path`.

## Final summary

- PR number and URL, and the SHA it ended on
- check rollup, per-context if anything is not green
- what you fixed, one line each
- restarts used
- feedback you replied to, and anything still open
- if it is not green: what is failing and what a human needs to decide
