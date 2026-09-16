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
              ├─ agent: claude -p, commit, push                           ├─ wait for the pipeline
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
bundled workflows: this install needs a wait-and-fix loop driven by Woodpecker,
which nothing shipped upstream provides — bundled workflows are GitHub-only, and
the ones that do wait on CI fail the run on a red check rather than fixing it
forward.

Trigger it with a message describing the change — that message is the agent's
entire brief.

| node | what it does |
| --- | --- |
| `implement` | Sandbox off `$BASE_BRANCH`; agent edits, commits, pushes `agent/<run>`. Prints the SHA. |
| `open-pr` | `POST /api/v1/repos/ops/k8s-homelab/pulls`. Reuses an already-open PR for the branch. |
| `ci-loop` → `ci-verdict` | Reads the verdict the relay recorded for the branch head. Prints `none`, `success` or `failure`. On failure, writes the tail of the failing Woodpecker step logs to `$ARTIFACTS_DIR/ci-failure.log`. |
| `ci-loop` → `fix` | Runs only `when: $ci-verdict.output == 'failure'`. New Sandbox with the **branch** checked out and that log mounted at `/task/ci-log.txt`. Pushes a fix. |
| `ci-loop` → `arm-ci` | Resolves the branch head from the server and writes the record that lets the relay map a commit back to this run. |
| `ci-loop` → `await-ci` | A **durable wait** on the `ci.complete` event. Archon persists the deadline and releases the worker; nothing polls. |
| `summary` | One JSON object: PR number and URL, branch, green commit, attempt count. |

`until_bash: ci-green.sh` ends the loop; `max_iterations: 6` bounds it —
6 pipelines waited on, so up to 5 fix attempts. Exhausting the bound **fails**
the run rather than reporting a still-red PR as done.

A red build is a *successful* run of `ci-verdict.sh` — the verdict is its
stdout, not its exit code. It exits non-zero only when the verdict cannot be
determined at all, which stops the run.

### The CI loop

The loop used to poll Forgejo's commit-status API every 20s for up to 45
minutes. It now blocks on an Archon durable wait that Woodpecker releases.

Getting there took a detour, because **Woodpecker has no outgoing webhooks**.
There is no server-level "notify X when a pipeline finishes"; upstream issue
[#4337][wp4337] was closed by restoring `CI_PIPELINE_STATUS` for pipeline
*steps* instead. The Woodpecker-native way to notify anything is a step in the
repo's own pipeline, so that is what `.woodpecker/ci.yaml` does.

[wp4337]: https://github.com/woodpecker-ci/woodpecker/issues/4337

```
 woodpecker agent pod (ns: woodpecker)
   └─ step: notify-archon      when: status: [success, failure]
        POST http://archon-ci-signal.archon.svc:9100/ci
        X-CI-Signature: HMAC-SHA256(body, $archon_ci_signal_secret)
             │   NetworkPolicy: the woodpecker namespace, this port, nothing else
             ▼
 archon pod
   ├─ ci-signal sidecar
   │    ├─ verify HMAC, then read /.archon/ci-events/waits/<sha>.json
   │    ├─ write  /.archon/ci-events/verdicts/<sha>.json
   │    └─ POST 127.0.0.1:3000/api/workflows/runs/<run>/signal   (loopback)
   └─ archon
        └─ await-ci wait node resumes
```

Three things about that shape are deliberate:

- **The relay is a sidecar, not its own Deployment.** It shares `/.archon`,
  which is ReadWriteOnce, with the workflow's bash nodes; and it signals Archon
  over **loopback**, which is the only place `ARCHON_WEB_AUTH_HEADER` can be
  trusted (see "Security posture"). Woodpecker reaches `:9100` here and never
  Archon's `:3000`.
- **Correlation is by commit SHA.** Woodpecker's notification carries a commit
  and nothing else that means anything to Archon, so `arm-ci.sh` writes the
  mapping from that commit to the run id just before the wait node starts. A
  40-character hex string also needs no escaping to be used as a filename, and
  the relay rejects anything that is not one.
- **Both sides of the race are handled.** A fast pipeline can finish before the
  wait node is even reached, so the relay retries the signal until the run
  reports an open `ci.complete` wait. A commit that no run is waiting for — every
  other PR on the repo also notifies — is dropped after `armGraceSeconds`.

If the notification never arrives, the wait expires on its own `deadline_ms` and
`ci-verdict.sh` fails the run on the next iteration saying so, rather than
looping on a commit nothing is going to report on.

`await-ci` is the **last** node in the loop body because a `loop_group` supports
a wait only as the sole terminal sink of its body. That is why the fix happens
at the top of the *next* iteration rather than after the wait in the same one.

### Where the code lives

Everything under `files/workflow/` is rendered into one ConfigMap
(`templates/configmap-workflow.yaml`) and mounted in two places:

- the Archon pod, at `/opt/archon-workflow` (scripts + the Sandbox template) and
  at `/home/appuser/.archon/workflows/homelab` (just the workflow definition —
  Archon's discovery walks `~/.archon/workflows` treating every `*.yaml` it
  finds there as a workflow, and `sandbox-pod.yaml` is a Kubernetes manifest).
  `~` is `/home/appuser` (the image runs as uid 1001), the `home` PVC — **not**
  `/.archon`, which is a different volume (the `data` PVC, used for
  `ci-events` state shared with the `ci-signal` sidecar);
- every Sandbox pod, at `/opt/agent` — but only `agent-entrypoint.sh` and
  `agent-prompt.md`, selected by `items` in `sandbox-pod.yaml`. The sandbox never
  sees the driver scripts or the API paths they use.

`files/ci-signal/` is its own ConfigMap (`templates/ci-signal.yaml`), mounted
read-only into the sidecar. The Deployment carries a checksum annotation over
each of the two, so editing either rolls the pod.

## Prerequisites

### 1. Secrets you create by hand

| Secret | Keys | Where from |
| --- | --- | --- |
| `archon-anthropic` | `CLAUDE_CODE_OAUTH_TOKEN` | `claude setup-token` |

```sh
kubectl -n archon create secret generic archon-anthropic \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=sk-ant-...
```

It is mounted by name into the sandbox pod template; the Archon ServiceAccount
has no `get` on Secrets, so it cannot read it.

### 2. Secrets provisioned for you

- `archon-forgejo-user` — written into this namespace by the `forgejo-resources`
  Job (see the `archon` entry under `forgejo-resources.users` in
  `k8s/platform/forgejo/values.yaml`). Keys: `username`, `password`, `token`.
  The account is a member of the `ops/Bots` team, which is what gives it push.
- `archon-woodpecker` — `token`, a Woodpecker personal access token for the
  `archon` account, written by the `woodpecker-resources` Job (see the `archon`
  entry under `woodpecker-resources.users` in
  `k8s/platform/woodpecker/values.yaml`). `ci-verdict.sh` reads the failing step
  logs with it. This used to be a hand-made token from the Woodpecker UI:
  Woodpecker exposes no API that mints a token for another user, so that job
  gets one by replaying the OAuth login as the account.
- `archon-ci-signal` — `hmac-secret`, the shared key between the notify step in
  `.woodpecker/ci.yaml` and the `ci-signal` sidecar. Generated by the same Job,
  which pushes the identical value into Woodpecker as the
  `archon_ci_signal_secret` repo secret.
- `archon-oauth2-proxy-secrets` — `client-secret` and `cookie-secret`
  autogenerated by mittwald secret-generator, then bcrypt-hashed into Authelia's
  aggregate Secret by the auth-system PreSync job.
- `archon-webhook` — autogenerated shared secret for the Forgejo webhook.
- `archon-psql-app` — CNPG.

### 3. Authelia

Already wired in `k8s/platform/auth-system/values.yaml`: the `archon` OIDC
client, the `archon` authorization policy, the `oidcClientSecrets.clients`
entry, the `archon-admins` LLDAP group, and the projected key in `secret.additionalSecrets`.

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

The web UI calls this a **Project**, not a codebase, but that's frontend only.
The API, DB schema, and every server-side log line still say `codebase`.

```sh
kubectl -n archon exec -it deploy/archon -- \
  curl -s -X POST http://127.0.0.1:3000/api/codebases \
    -H 'X-Auth-Request-User: <your-authelia-username>' \
    -H 'Content-Type: application/json' \
    -d '{"url": "https://git.msng.to/ops/k8s-homelab.git"}'
```

The workflow itself never touches that checkout (`mutates_checkout: false`) —
all the real work happens in the sandbox — but Archon wants one to exist.

While you are in there, turn off the 19 bundled workflows: they're GitHub-shaped
and would only clutter the router's choices for an install that runs one
workflow against one Forgejo repo. The setting lives in
`/home/appuser/.archon/config.yaml` — **not** `/.archon/config.yaml`; `~` is
`/home/appuser` (the image runs as uid 1001), on the `home` PVC, a different
volume from `/.archon` (the `data` PVC) — which Archon writes itself, so it is
deliberately not templated by this chart. Archon writes it as a single-line
flow-style mapping, so append a raw `defaults:` block rather than `>>`-ing one
on — two top-level nodes in one YAML document is invalid and Archon will
silently fall back to defaults on a parse error. Inject the key into the
existing flow mapping instead:

```sh
kubectl -n archon exec -it deploy/archon -- \
  sh -c 'sed -i "s/^{/{defaults:{loadDefaultWorkflows:false},/" /home/appuser/.archon/config.yaml'
kubectl -n archon rollout restart deploy/archon
```

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
- **The CI-signal port is in-cluster only.** `:9100` has no HTTPRoute and no
  ingress; an HMAC over the raw body authenticates the caller. The two ingress
  rules on the Archon NetworkPolicy are each scoped to one port, so Woodpecker
  reaches `:9100` and nothing else — which is what the next bullet depends on.
- **SSO, two-factor, two groups.** Authelia's `archon` policy is
  `default_policy: deny` with `two_factor` for `archon-admins` and `full-admin`;
  oauth2-proxy enforces the same two groups again.
- **Trusted-header auth is fenced.** Archon reads a header set by oauth2-proxy
  and believes it, so anything that could reach the app port directly could
  claim to be any user — the NetworkPolicy is what makes that safe, by making
  oauth2-proxy the sole ingress path. The `ci-signal` sidecar sets that header
  too, but only ever on `127.0.0.1`, from inside the same pod.
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

# Why a run is still sitting on its wait: did the notification arrive?
kubectl -n archon logs deploy/archon -c ci-signal
kubectl -n archon exec deploy/archon -c ci-signal -- \
  ls /.archon/ci-events/waits /.archon/ci-events/verdicts
```

A wait with a record under `waits/` and nothing under `verdicts/` means the
notify step never reached the relay. Check the `notify-archon` step in the
pipeline's own log first — it prints the failure rather than failing the build.

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
- **Two credentials are obtained by scripted OAuth.** Woodpecker has no API
  that mints a personal access token for another user — a token is a JWT signed
  with the user's `hash` column, which `model.User` tags `json:"-"` — so the
  `woodpecker-resources` Job replays the browser login, parsing two Forgejo HTML
  forms on the way through. It is the most fragile thing in this stack, which is
  why it runs only when the stored token no longer authenticates, and why those
  two parsers have unit tests.
- **The Gitea adapter is community-maintained**, not first-party like the GitHub
  one. If it regresses, the web UI still triggers runs and the workflow itself
  never touches the adapter — it talks to Forgejo over the REST API directly.
