# archon

[Archon][upstream] as the control plane for one job: take a plain-English
description of a change to `ops/k8s-homelab`, have Claude Code make it inside a
single-use Kubernetes sandbox, open a pull request on `git.msng.to`, and keep
fixing the branch until the Woodpecker PR check is green.

[upstream]: https://github.com/coleam00/archon

## Shape

```
  archon.msng.to ──► oauth2-proxy (Authelia OIDC) ──► archon :3000
  (internal VIP)                                        │
                                                        │ workflow: homelab-sandbox-pr
                                                        ▼
                        ┌──────────────── bash nodes, kubectl + REST ──────────────┐
                        │                                                          │
                        ▼                                                          ▼
              Sandbox (agents.x-k8s.io)                                   Forgejo + Woodpecker
              ├─ init: clone ops/k8s-homelab                              ├─ create the PR
              ├─ agent: claude -p, commit, push                           ├─ poll the commit status
              └─ ServiceAccount: cluster READ only                        └─ pull the failing step logs
```

Runs start one of two ways: a comment on an issue or PR in Forgejo (the
community **Gitea adapter**, which Forgejo satisfies — same API, same
`X-Gitea-Signature` HMAC), or the web UI behind SSO.

```
@archon add a NetworkPolicy to the spoolman chart
@archon /workflow run homelab-sandbox-pr bump the joplin chart to 3.8.0
```

The adapter wakes on `@archon` in a **comment** — not in an issue body — clones
the repo on first mention, registers it as a codebase, and hands the message to
the workflow router, which picks `homelab-sandbox-pr` from its `description`.
Replies come back as comments on the same thread.

The split matters. **Archon holds the credentials and the run state; the sandbox
holds the model.** The Archon pod has a namespace-scoped ServiceAccount that can
manage Sandboxes and read pod logs, and it holds the Forgejo and Woodpecker
tokens. The sandbox pod has a *different* ServiceAccount with cluster-wide
read-only access and no Secret access at all, plus a repo-scoped Forgejo token
for the one push it makes. Neither can do the other's job.

Archon does **not** run Claude Code itself here. Every node in the workflow is a
`bash:` node; the model only ever runs as a sandbox pod's entrypoint.

## The workflow

`files/workflow/homelab-sandbox-pr.yaml` is custom rather than one of Archon's
bundled workflows: this install needs a poll-and-fix loop against Forgejo and
Woodpecker's commit-status API, which nothing shipped upstream provides —
bundled workflows are GitHub-only, and the ones that do wait on CI fail the run
on a red check rather than fixing it forward.

Trigger it with a message describing the change — that message is the agent's
entire brief.

| node | what it does |
| --- | --- |
| `implement` | Sandbox off `$BASE_BRANCH`; agent edits, commits, pushes `agent/<run>`. Prints the SHA. |
| `open-pr` | `POST /api/v1/repos/ops/k8s-homelab/pulls`. Reuses an already-open PR for the branch. |
| `ci-loop` → `await-ci` | Polls the Forgejo commit status for the branch head until every `ci/woodpecker/pr*` check is terminal. Prints `success` or `failure`. On failure, writes the tail of the failing Woodpecker step logs to `$ARTIFACTS_DIR/ci-failure.log`. |
| `ci-loop` → `fix` | Runs only `when: $await-ci.output != 'success'`. New Sandbox with the **branch** checked out and that log mounted at `/task/ci-log.txt`. Pushes a fix. |
| `summary` | One JSON object: PR number and URL, branch, green commit, attempt count. |

`until_bash: test "$await-ci.output" = "success"` ends the loop;
`max_iterations: 5` bounds it. Exhausting the bound **fails** the run rather than
reporting a still-red PR as done.

A red build is a *successful* run of `await-ci.sh` — the verdict is its stdout,
not its exit code. It exits non-zero only when the verdict cannot be determined
at all (API error, no status ever reported, timeout), which stops the run.

### Where the code lives

Everything under `files/workflow/` is rendered into one ConfigMap
(`templates/configmap-workflow.yaml`) and mounted in two places:

- the Archon pod, at `/opt/archon-workflow` (scripts + the Sandbox template) and
  at `/.archon/workflows/homelab` (just the workflow definition — Archon's
  discovery treats every `*.yaml` it finds there as a workflow, and
  `sandbox-pod.yaml` is a Kubernetes manifest);
- every Sandbox pod, at `/opt/agent` — but only `agent-entrypoint.sh` and
  `agent-prompt.md`, selected by `items` in `sandbox-pod.yaml`. The sandbox never
  sees the driver scripts or the API paths they use.

The Deployment carries a checksum annotation over the ConfigMap, so editing a
script here rolls the pod.

## Prerequisites

### 1. Secrets you create by hand

| Secret | Keys | Where from |
| --- | --- | --- |
| `archon-anthropic` | `ANTHROPIC_API_KEY` **or** `CLAUDE_CODE_OAUTH_TOKEN` | console.anthropic.com, or `claude setup-token` |
| `archon-woodpecker` | `token` | Woodpecker UI → user settings → personal access token, for a user with pull access to `ops/k8s-homelab` |

```sh
kubectl -n archon create secret generic archon-anthropic \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...
kubectl -n archon create secret generic archon-woodpecker \
  --from-literal=token=...
```

Both are mounted by name into the sandbox pod template; the Archon
ServiceAccount has no `get` on Secrets, so it cannot read either.

### 2. Secrets provisioned for you

- `archon-forgejo-user` — written into this namespace by the `forgejo-resources`
  Job (see the `archon` entry under `forgejo-resources.users` in
  `k8s/platform/forgejo/values.yaml`). Keys: `username`, `password`, `token`.
  The account is a member of the `ops/Bots` team, which is what gives it push.
- `archon-oauth2-proxy-secrets` — `client-secret` and `cookie-secret`
  autogenerated by mittwald secret-generator, then bcrypt-hashed into Authelia's
  aggregate Secret by the auth-system PreSync job.
- `archon-webhook` — autogenerated shared secret for the Forgejo webhook.
- `archon-psql-app` — CNPG.

### 3. Authelia

Already wired in `k8s/platform/auth-system/values.yaml`: the `archon` OIDC
client, the `archon` authorization policy, the `oidcClientSecrets.clients`
entry, and the projected key in `secret.additionalSecrets`. What is **not**
automatic is the LLDAP group — create `archon-admins` and put yourself in it, or
rely on `full-admin`.

### 4. The sandbox image

`files/image/Dockerfile`. Built and pushed out of band, like
`k8s/apps/redteam`'s session image:

```sh
docker buildx build --platform linux/amd64 \
  -t ghcr.io/teekennedy/k8s-homelab-agent:v0.1.0 \
  --push k8s/apps/archon/files/image
```

Then bump `sandbox.image` in `values.yaml`. Keep tags immutable — the pods use
`IfNotPresent`, so a moving tag means two attempts in one run can disagree about
what "the agent" is.

### 5. Register the codebase

Only needed if you intend to start runs from the **web UI**. Triggering through
the Gitea adapter does this for you: the first `@archon` mention on a repo
clones it into `/.archon/workspaces/` and registers it as a codebase.

```sh
kubectl -n archon exec -it deploy/archon -- \
  bun run archon codebase add k8s-homelab https://git.msng.to/ops/k8s-homelab.git
```

The workflow itself never touches that checkout (`mutates_checkout: false`) —
all the real work happens in the sandbox — but Archon wants one to exist.

While you are in there, turn off the 19 bundled workflows: they're GitHub-shaped
and would only clutter the router's choices for an install that runs one
workflow against one Forgejo repo. The setting lives in `/.archon/config.yaml`,
which is on the data PVC and which Archon writes itself, so it is deliberately
not templated by this chart:

```sh
kubectl -n archon exec -it deploy/archon -- \
  sh -c 'printf "defaults:\n  loadDefaultWorkflows: false\n" >> /.archon/config.yaml'
kubectl -n archon rollout restart deploy/archon
```

### 6. The Forgejo webhook

Nothing to do — it is provisioned. This is what turns a comment into a run, and
it is declared in `k8s/platform/forgejo/values.yaml` under
`forgejo-resources.repositories[].webhooks`:

```yaml
- url: https://archon.msng.to/webhooks/gitea
  type: gitea            # X-Gitea-Signature; `gogs` would sign the wrong header
  events: [issues, issue_comment, pull_request, pull_request_comment]
  secretName: archon-webhook
  secretNamespace: archon
  secretKey: webhook-secret
```

The `forgejo-resources` Job generates the shared secret, writes it into this
namespace's `archon-webhook` Secret, and registers the hook with the same value.
On a cold bootstrap the Job may run before this namespace exists — it logs the
failure for that one hook and registers it on the next forgejo sync.

## Security posture

The thing worth being careful about: **this UI can make a model push to the repo
that defines the cluster.**

- **Internal only.** The HTTPRoute attaches to the internal gateway; the
  namespace carries `internal-gateway-access`, never `external-gateway-access`.
- **One path is outside SSO**, by necessity: `POST /webhooks/gitea`. A webhook
  cannot carry an Authelia session. What authenticates a request there instead
  is the `X-Gitea-Signature` HMAC over the raw body, plus an allowlist of
  Forgejo usernames on the comment's author. Everything else, `/api/*`
  included, still requires a session.
- **SSO, two-factor, two groups.** Authelia's `archon` policy is
  `default_policy: deny` with `two_factor` for `archon-admins` and `full-admin`;
  oauth2-proxy enforces the same two groups again.
- **Trusted-header auth is fenced.** Archon reads a header set by oauth2-proxy
  and believes it, so anything that could reach the app port directly could
  claim to be any user — the NetworkPolicy is what makes that safe, by making
  oauth2-proxy the sole ingress path.
- **The agent gets read, not write.** Its ClusterRole is an explicit allowlist
  with no `secrets` grant. RBAC has no deny verb, so that omission *is* the
  control.
- **The sandbox is single-use and network-fenced.** No restart, a bounded
  lifetime, its PVC deleted with it, no ingress at all, and egress limited to
  DNS, the kube API, the LAN, and `:443`.
- **Nothing merges itself.** The workflow opens a PR and gets it green. A human
  still reviews and merges.

`--dangerously-skip-permissions` is used inside the sandbox — there is no human
to approve tool calls in a headless run. What bounds it is the sandbox, not the
flag.

## Operating it

```sh
# What's running right now
kubectl -n archon get sandboxes

# Why an attempt failed (the driver copies these into the run's artifacts, but
# they are also in the pod log until the Sandbox is deleted)
kubectl -n archon logs pod/archon-<run>-<attempt> -c agent

# Run artifacts: task.md, the rendered Sandbox, agent logs, ci-failure.log
kubectl -n archon exec deploy/archon -- ls /.archon/workspaces
```

A leaked Sandbox (driver killed between create and its EXIT trap) holds a
Longhorn volume and a model-sized pod. They are labelled, so:

```sh
kubectl -n archon delete sandbox -l app.kubernetes.io/part-of=archon-sandbox
```

## Known rough edges

- **`readOnlyRootFilesystem: false`** on the Archon container. The image's
  entrypoint and bun both write outside the volumes. The sandbox container,
  which is the part that runs untrusted output, *is* read-only.
- **Single replica.** `/.archon` and `/home/appuser` are RWO and Archon's run
  state assumes one writer.
- **The Gitea adapter is community-maintained**, not first-party like the GitHub
  one. If it regresses, the web UI still triggers runs and the workflow itself
  never touches the adapter — it talks to Forgejo over the REST API directly.
