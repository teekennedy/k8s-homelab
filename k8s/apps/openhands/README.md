# openhands

[OpenHands Agent Canvas][upstream] as the agent control plane: a web UI for
starting agent sessions, the history of every session that has run, and the
automation backend that will later run them on a schedule or on an event. The
model itself is not OpenHands' — sessions drive **Claude Code** as an external
agent over [ACP][acp].

[upstream]: https://github.com/OpenHands/OpenHands
[acp]: https://agentclientprotocol.com/protocol/overview

## Shape

```
  openhands.msng.to ──► oauth2-proxy (Authelia OIDC) ──► canvas pod :8000
  (internal VIP)                                            │
                                     ┌──────────────────────┼─────────────────────┐
                                     ▼                      ▼                     ▼
                              static frontend      /api, /runtime ──► app server  /api/automation/v1/events
                              (locked to the                         │   │        ──► automation :18001
                               app server's                          │   └─► automation :18001 (with its key)
                               cloud API)                            ▼
                                                   Sandbox per conversation (agents.x-k8s.io)
                                                   agent server :8000 ──► claude-agent-acp ──► Anthropic API
                                                        │ webhooks: events, status
                                                        └────────────► app server :8081
```

The frontend runs in **locked-cloud mode**: the static server is started with
`--lock-to-cloud https://openhands.msng.to`, so every browser gets exactly one
backend — this origin's cloud API, served by the app server — with no
`localStorage` setup and no API key, because a cloud backend on the page's own
origin authenticates by cookie, and that cookie is the oauth2-proxy session.

**Each conversation runs in its own pod**, a `Sandbox` the app server creates
from a sandbox spec: its own agent server, workspace volume, session key and
encryption key, under the spec's ServiceAccount and NetworkPolicy. The browser
reaches it at `/runtime/<sandbox>` through the app server. History is posted
back to the app server by webhook as it happens, so a conversation stays
readable after its sandbox is gone.

The canvas pod runs the static frontend and the automation service, and no
agent: sandboxes are the agent servers. Its entrypoint is
`files/canvas/start.sh` rather than the image's, because the image's has no way
to pass `--lock-to-cloud`.

## Security posture

`openhands.msng.to` resolves to the internal VIP and the namespace carries
`internal-gateway-access: "true"`, never `external-gateway-access`.

The page carries no credential. The app server accepts a browser on the user
header oauth2-proxy sets, and hands it each conversation's own session key; it
also forwards the browser's automation calls with that service's key. There is
no user model and no per-user authorization behind that, so oauth2-proxy is not
one layer of defence among several — it is the access control, and the
NetworkPolicies are what stop anything else in the cluster from bypassing it:
the app server's API port admits the canvas pod alone, and a sandbox admits the
app server alone.

Three consequences worth stating plainly:

- **Anyone in `openhands-admins` or `full-admin` has a shell in every
  sandbox**, through the agent. Both groups are `two_factor` in the `openhands`
  authorization policy; there is no viewer tier, because there is nothing a
  viewer could be restricted to.
- **A sandbox cannot reach the app server's API**, only its webhook port, and
  cannot reach the Kubernetes API: an agent cannot start more agents.
- **Only the app server talks to the Kubernetes API**, and only to manage
  sandboxes — see `templates/app-server-rbac.yaml`. Every other ServiceAccount
  in this chart has no permissions and no mounted token.

Sandbox egress is per spec (see "Sandbox specs"). The canvas pod's is DNS and the
app server. Everything else, including the API server, the forge, the internet
and every other namespace, is denied.

## Prerequisites

### 1. Secrets you create by hand

| Secret | Keys | Where from |
| --- | --- | --- |
| `openhands-anthropic` | `CLAUDE_CODE_OAUTH_TOKEN` | `claude setup-token` |

```sh
kubectl -n openhands create secret generic openhands-anthropic \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=sk-ant-...
```

Every key in it is exported into the container's environment, and from there
into the ACP subprocess. `ANTHROPIC_API_KEY` works too and bills per token
instead of against the subscription.

> Do **not** put `ANTHROPIC_BASE_URL` in this Secret. It routes the request away
> from Anthropic and silently breaks the OAuth token's bearer auth.

### 2. Secrets provisioned for you

- `openhands-forgejo-user` — written into this namespace by the
  `forgejo-resources` Job (see the `openhands` entry under
  `forgejo-resources.users` in `k8s/platform/forgejo/values.yaml`). Keys:
  `username`, `password`, `token`. The account is a member of the `ops/Bots`
  team, which is what gives it push.
- `openhands-oauth2-proxy-secrets` — `client-secret` and `cookie-secret`
  autogenerated by mittwald secret-generator, then bcrypt-hashed into Authelia's
  aggregate Secret by the auth-system PreSync job.

### 3. Authelia

Already wired in `k8s/platform/auth-system/values.yaml`: the `openhands` OIDC
client, the `openhands` authorization policy, the `oidcClientSecrets.clients`
entry, the `openhands-admins` LLDAP group, and the projected key in
`secret.additionalSecrets`.

## Git credentials

The agent pushes as the `openhands` Forgejo account. `GIT_CONFIG_GLOBAL` points
at a ConfigMap-mounted gitconfig whose credential helper echoes
`$FORGEJO_USERNAME` / `$FORGEJO_TOKEN`, both of which come from
`openhands-forgejo-user` as environment variables.

Nothing writes a token to disk and nothing has to be configured in the UI — a
`git clone`/`git push` against `git.msng.to` authenticates on its own. The
mounted file is read-only, so a session that wants a different global setting
has to set it per-repository.

## First run

Nothing to do. Settings and the agent profile are seeded from
`appServer.defaults` into the app server's database on first boot — Claude Code
over ACP — and from then on are owned by the Settings UI, except for the
launch command and default model (see "Models"). The frontend's own
first-run wizard is a per-browser `localStorage` flag; close it once per
browser.

Codex and Gemini CLI ship as presets alongside Claude Code, and **Custom**
accepts the launch command of any stdio ACP server; credentials for them have
to reach the sandbox pod's environment, as `openhands-anthropic` does.

## Skills

`files/skills/` holds Agent Skills that the chart seeds into the agent's home on
every pod start, so git is the source of truth and a change takes effect on the
next rollout. Every sandbox gets the same set.

They go to **two** directories, because two different readers look for them and
neither reads the other's path:

| Path | Read by |
| --- | --- |
| `~/.agents/skills/` | the agent server's user-skill search, which is what lists a skill in the UI under **Skills** and puts it in the agent context |
| `~/.claude/skills/` | the Claude Code CLI that an ACP session spawns, natively |

Seeding only the second is a silent failure: the skill still works inside a
Claude Code session, but nothing in OpenHands knows it exists, so it appears
neither as installed nor as installable. The agent server searches
`~/.agents/skills/`, `~/.openhands/skills/` and `~/.openhands/microagents/`, in
that order; `~/.agents/skills/` is the one this chart uses because the other two
are on the data volume, where a removed skill would outlive git.

They are copied in by an initContainer rather than mounted. A ConfigMap mounted
at either path would make that directory root-owned and read-only, and the agent
writes its own state beside the skills in both. Both directories are `emptyDir`,
so the copy is a full refresh and a deleted skill does not linger.

| Skill | What it does |
| --- | --- |
| `forgejo-iterate` | Drives a Forgejo pull request to green: polls the combined commit status, pulls failing step logs out of Woodpecker, classifies the failure, fixes or restarts, and repeats. |

The ACP adapter passes `settingSources: ["user", "project", "local"]` to the
agent, which is what makes a skill in the home directory load at all. Nothing in
`forgejo-iterate` is specific to this deployment — it is `curl` and `jq` against
two HTTP APIs, and works in any Claude Code session with the same environment
variables set.

## Remote Control

`remoteControl.enabled` adds a second, long-lived pod running
`claude remote-control` in server mode, so a session can be driven from
claude.ai/code or the Claude mobile app. It shares only the namespace and the
model credential with the canvas.

It has to be a separate pod. Remote Control refuses to start when any of
`DO_NOT_TRACK`, `DISABLE_TELEMETRY`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`
or `DISABLE_GROWTHBOOK` is set, and the canvas sets the first of those on
purpose — one process cannot have both.

Three things in that pod spec are load-bearing, and each one silently disables
Remote Control if it goes missing:

- **A subscription login.** `CLAUDE_CODE_OAUTH_TOKEN`, not an API key.
- **No `ANTHROPIC_BASE_URL`**, anywhere — including in the credential Secret.
- **Workspace trust, pre-accepted.** A pod has no terminal to answer the trust
  dialog with, so the initContainer writes the decision into `~/.claude.json`
  for the checkout directory before the CLI ever starts.

Default off: whether a `claude setup-token` OAuth token counts as an eligible
subscription login is unverified. The pod exits with "You must be logged in to
use Remote Control" if it does not.

## Sandbox specs

A sandbox spec is the isolation a conversation runs under: a ServiceAccount, a
NetworkPolicy, resource limits, a volume size and which credentials are in the
pod. They are `sandboxSpecs.specs` in `values.yaml`.

| Spec | Reaches | Forge credentials |
| --- | --- | --- |
| `repo` (default) | the model API, `git.msng.to`, the API server and the Dagger engine | yes |
| `isolated` | the model API only — nothing on the LAN | no |

A spec with `daggerEngine: true` gets a mounted ServiceAccount token, a Role
granting `pods/exec` in the `dagger-engine` namespace, and egress to the API
server on 6443. That is what Dagger's `kube-pod://` runner transport needs, as
for Woodpecker's `dagger-pipeline`. The sandbox image must supply `kubectl` and
the `dagger` CLI, which the sandbox image provides, and sets
`_EXPERIMENTAL_DAGGER_RUNNER_HOST` for it.

**The agent profile picker chooses the spec.** A conversation runs on the spec
its agent profile is named after — `isolated`, or `isolated-<anything>` — and
on `sandboxSpecs.default` for a profile named anything else. The app server
keeps a profile named after every spec but the default, copied from
`appServer.defaults.agent_profile`, so `isolated` is in the picker without
anyone creating it; deleting it in the UI lasts until the app server restarts.
A second agent on the same spec is a profile called, say, `isolated-sonnet`.

Two things follow from matching on the name:

- **Renaming a profile can change its isolation.** `isolated` renamed to
  `scratch` runs on the default spec, with forge credentials, from the next
  conversation on. A running conversation keeps the sandbox it has.
- A conversation started with no profile uses the active one, so activating
  `isolated` makes it the spec for new conversations.

Which spec a sandbox got is its `openhands.msng.to/sandbox-spec` label:

```sh
kubectl -n openhands get sandbox -L openhands.msng.to/sandbox-spec
```

### Sandbox image

`files/sandbox/Dockerfile` is the upstream agent server with `kubectl` and the
`dagger` CLI added; the CLI version must match the engine's. To publish a new
one (after bumping the `FROM` tag or either CLI version):

```sh
echo "$GHCR_TOKEN" | docker login ghcr.io -u teekennedy --password-stdin
docker buildx build --platform linux/amd64 \
  -t ghcr.io/teekennedy/openhands-sandbox:<tag> --push files/sandbox
```

then set `sandboxSpecs.image` to `<tag>`. Keep `claudeAdapterVersion` equal to the Dockerfile's `CLAUDE_ADAPTER_VERSION`; the chart only uses it as a label. The token needs `write:packages`, and
the package must be readable by the cluster (public, or an imagePullSecret).

### Models

The model picker's entries are fixed in the frontend, and all but one are
aliases — `opus[1m]`, `sonnet`, `haiku` — that the Claude Code CLI in the
sandbox resolves to the newest model it knows. The CLI also refuses a model
newer than itself. So which models a sandbox runs is a matter of which CLI it
has, and nothing here names a model.

The Claude Code ACP adapter, which carries the CLI, is baked into the sandbox
image (`CLAUDE_ADAPTER_VERSION` in `files/sandbox/Dockerfile`), so a sandbox
starts without installing anything. Bumping it is how a new model arrives:
change that ARG and `sandboxSpecs.claudeAdapterVersion` together, then rebuild
and push the image as under "Sandbox image".

Two things about the Claude Code agent are declared in `values.yaml`, under
`appServer.acpServers.claude-code`, rather than left to the Settings UI:

- `command` is what a conversation launches: `claude-agent-acp`, found on the
  sandbox's `PATH`, whatever a profile carries. The agent profile form shows
  something else for it — the frontend's built-in command for the preset,
  `npx … claude-agent-acp@<version>` — because that exact text is how the
  form knows the profile is Claude Code and not "Custom". So that it at least
  names the right version, the canvas pod serves a copy of the frontend with
  `claudeAdapterVersion` written into that command
  (`files/canvas/patch_frontend.py`). The copy's assets are renamed with the
  version, because they are served as immutable and a browser would otherwise
  keep the old ones. If a canvas release changes how the command is written,
  the pod logs that it found nothing to rewrite and serves the frontend as
  shipped.
- `model` is the model new conversations start on. Changing it rewrites the
  stored settings and every Claude Code profile once; after that the model
  picker's choice stands until the value here changes again.

## App server

`files/app-server/` is a small FastAPI service, `openhands-app-server`, that
runs one [agent-sandbox][agent-sandbox] `Sandbox` per sandbox id and serves the
cloud API the frontend speaks in locked-cloud mode. The source is mounted from a
ConfigMap into the `uv` image and its locked dependencies are installed at
start, so there is no image of our own to build.

[agent-sandbox]: https://github.com/kubernetes-sigs/agent-sandbox

A sandbox is instantiated from one of `sandboxSpecs.specs`, which Helm renders
into the `openhands-sandbox-specs` ConfigMap as complete `Sandbox` manifests.
Per sandbox, the app server mints a session key and an encryption key into a
`<id>-config` Secret that the `Sandbox` owns; the `Sandbox` in turn owns its
pod, its headless Service and its workspace PVC, so deleting the `Sandbox` is
the entire cleanup. Pause and resume flip `spec.operatingMode` between
`Running` and `Suspended`, which deletes and recreates the pod while keeping the
volume.

| Path | Caller | Authenticated by |
| --- | --- | --- |
| `/api/v1/sandboxes`, `/api/v1/sandboxes/{id}/{pause,resume}` | browser, automation service | `X-Forwarded-User` from oauth2-proxy, or a minted bearer key |
| `/api/service/users/{uid}/orgs/{oid}/api-keys` | automation service | `X-Service-API-Key` from `openhands-app-server-keys` |
| `/runtime/{id}/**` | browser, automation service | as above, or that sandbox's own session key |
| `/api/v1/app-conversations/**`, `/api/v1/conversation/{id}/events/**` | browser | `X-Forwarded-User` |
| `/api/v1/settings/**`, `/api/agent-profiles/**`, `/api/v1/secrets/**` | browser | `X-Forwarded-User` |
| `/api/v1/git/{repositories,branches}/search` | browser | `X-Forwarded-User`; answered from the forge's API |
| `/api/v1/app-conversations/{id}/{files,file,download,skills,git/*,switch_acp_model}` | browser | `X-Forwarded-User`; answered by the conversation's own agent server |
| `/api/automation/**` | browser | `X-Forwarded-User`; forwarded with the automation service's key |
| `/login`, `/canvas/login` | browser | `X-Forwarded-User`; redirects to `returnTo`, see below |
| `:8081/sandboxes/{id}/{events,conversations}` | the sandbox's agent server | that sandbox's own session key |

`/login` is where the frontend sends the browser when its session check gets a
401, which is what oauth2-proxy answers an XHR once its session has lapsed.
The frontend has no such route of its own, so the static server routes it
here. That navigation is what makes oauth2-proxy sign the browser in again, so
by the time the request arrives there is nothing left to do but redirect to
`returnTo`, if it is a path on this origin.

`/runtime/{id}` strips the prefix and proxies HTTP and WebSockets to the
sandbox's agent server. It drops `Cookie`, `Authorization` and every
`X-Forwarded-*` header first: an agent server runs code the agent controls, and
the oauth2-proxy cookie must not reach it.

What the app server may do in the cluster is in
`templates/app-server-rbac.yaml`. The one grant worth reading twice is Secrets:
`create` only. It never reads back the keys it minted, because it keeps them in
its own database, and it cannot read the model credential — sandboxes get that
by reference in the pod template. The forge token is the exception: it is in
the app server's own environment, for the repository picker below.

Starting a conversation is a start task the frontend polls: the app server
creates a sandbox from the spec the agent profile names (see "Sandbox
specs"), waits for it to be `RUNNING`, then
starts the conversation on the sandbox's agent server with the stored
`agent_settings` and the chosen ACP agent profile laid over them. Titles are set
here from the first message — the agent server's own titling uses the agent's
LLM, which for an ACP agent is not one litellm can call.

**Open Repository** and **Connect Repo** search the forge. The settings the
app server returns name one git provider, `forgejo`, with the forge's host;
that is what enables the picker. `/api/v1/git/repositories/search` and
`/api/v1/git/branches/search` then answer from `forge.url` alone, whatever
`provider` the request names — the frontend falls back to `github` when it has
not read the provider list — as the `openhands` forge account, so the picker
offers exactly what a sandbox can clone. A conversation started on a
repository has it fetched into `/workspace/project` and the chosen branch
checked out, by the sandbox's own git credentials, before the agent starts; **Connect Repo** in
a running conversation instead asks the agent to clone it. Both need a spec
that reaches the forge: on `isolated` the start fails with git's error.

**Git actions** (Pull, Push, Create PR, Create New Branch) are not API calls:
each puts a canned request to the agent in the chat box, which the agent
carries out with the sandbox's git credentials and `FORGEJO_TOKEN`. The only
part the app server plays is the wording — every conversation reports
`git_provider: forgejo`, repository chosen or not, because the frontend
otherwise writes "push to GitHub".

Each sandbox's agent server posts its events and status changes to the app
server's webhook port; events are stored per conversation and served as the
conversation's history, so it outlives the sandbox. A conversation whose
sandbox is gone shows as archived (`MISSING`) and reads from that copy; one
whose sandbox is paused is resumed by the frontend when opened.

The per-conversation panels — files, file contents, git changes and diffs,
skills, the trajectory download, switching the ACP model — are each the agent
server call the frontend would make itself in local mode, made by the app server
on the conversation's own sandbox. With no running sandbox there is nothing to
ask: listings are empty and the conversation reads from history.

**Settings → Secrets** is a store in the app server's database, encrypted with
the autogenerated `openhands-app-server-secrets-key`. Every secret is handed to
each new conversation, and the agent sees it under its name — so a secret named
like an environment variable is how a credential reaches an agent without being
in the pod spec. Infrastructure credentials (`openhands-anthropic`, the forge
token) do not go through it: sandbox specs reference those Secrets directly, so
no browser ever handles them. Losing the key Secret
makes stored secrets undecryptable; they are re-entered, not restored.

The Settings UI renders itself from the agent server's settings schemas; an
initContainer asks the sandbox image's own agent server for them at pod start,
so they always match the version conversations run on.

A reconciler compares the database to the cluster every minute: a managed
`Sandbox` with no live row is deleted, and a row whose `Sandbox` has vanished
reports `MISSING`. It then applies the limits below.

## Automations

Scheduled and event-driven runs are defined in `values.yaml` under
`automations`, by name: a trigger, a prompt, and optionally a timeout.

```yaml
automations:
  nightly-triage:
    trigger: {type: cron, schedule: "0 6 * * *", timezone: America/Denver}
    prompt: |
      List the open issues on ops/k8s-homelab and label the unlabelled ones.
    timeout: 1800
```

The upstream automation service does the scheduling, the webhook matching and
the run history, in **cloud mode** against the app server: for each run it asks
the app server for a sandbox, starts the run in it, and deletes it when the run
reports back. So a run gets what a conversation gets — a pod, a workspace and a
session key of its own — and leaves nothing behind but its transcript.

What runs in that sandbox is ours. The automation service's own run scripts
build an agent from an LLM API key, and the only model credential here is the
Claude Code login, so every automation is a *custom* one running the same
small tarball (`app_server/automation_run.py`), which the app server uploads to
the automation service's own store. Its entry
point asks the app server to start the run's conversation, which is therefore
the same ACP agent, with the same credentials and secrets, as one started from
the UI — and is listed beside them, titled with the automation's name. It then
waits for the conversation to stop and reports the outcome.

```
  automation service ──► app server: POST /api/v1/sandboxes        (a sandbox for the run)
          │
          └─► sandbox: unpack the uploaded tarball, run `python3 run.py`
                 │
                 ├─► app server :8081  …/automation/conversations   (start; prompt from values)
                 ├─► own agent server  poll until the conversation stops
                 └─► app server :8081  …/automation/runs/<id>/complete ──► automation service
                                                                            └─► deletes the sandbox
```

The app server pushes the definitions to the automation service once a minute:
it creates what is missing, updates a changed trigger or timeout, and deletes
an automation that was removed from `values.yaml`. It recognises its own by the
tarball they run and leaves any other automation alone; a new version of the
run script is a new upload, and the automations are moved onto it.

- **Automations created in the UI do not work.** The "new automation" form
  creates the upstream kind, which fails for want of an LLM API key.
- **Every run uses the default sandbox spec.** The automation service asks for
  a sandbox without naming one.
- **`enabled: false` switches an automation off; nothing here switches one
  on.** The service disables an automation that keeps failing, and re-enabling
  it is a decision, made in the UI.
- A run that outlives its timeout (10 minutes unless set, 30 at most) is failed
  by the automation service and its sandbox deleted.

### Event triggers

An event trigger names a webhook *source* registered in the automation
service. The forge is one, set up without anyone handling a secret:

1. This chart ships an empty Secret, `openhands-forge-webhook`.
2. The `forgejo-resources` Job (`k8s/platform/forgejo`, the
   `repositories[].webhooks` entry for this URL) generates the shared secret
   into it and registers a `gitea`-typed webhook on `ops/k8s-homelab` that
   delivers to
   `/api/automation/v1/events/<organization id>/forgejo`.
3. The app server reads the Secret and registers the source `forgejo` with the
   same secret: hex HMAC-SHA256 of the raw body in `X-Gitea-Signature`, which
   is exactly what that webhook type sends. A rotated secret is picked up on
   the next sync.

An automation then triggers on it with
`trigger: {type: event, source: forgejo, on: <key>, filter: <JMESPath>}`.
The event key is the payload's `action` (`created`, `opened`, `closed`) and
nothing else, so it cannot tell a comment from an issue: the webhook is
subscribed to comment events only, and anything finer goes in `filter`, which
is evaluated against the whole payload. `forge-mention` in `values.yaml` is
the working example.

`oauth2-proxy` already skips authentication for `POST` on the event path — a
webhook carries no Authelia session. The HMAC is what authenticates a delivery;
the path answers 404 for a source nobody has registered.

> A comment-triggered automation on a **public** repo means anyone with a
> Forgejo account can start a run. The HMAC proves the delivery came from
> Forgejo, not that the commenter is allowed to spend model budget. A Forgejo
> webhook cannot filter on author, so the place to enforce one is the trigger's
> `filter`, as `forge-mention` does with `sender.login == '…'`.

## Limits

The app server's reconciler applies `appServer.limits` once a minute.

| Limit | Default | What happens |
| --- | --- | --- |
| `maxRunningSandboxes` | 8 | A new conversation fails with the reason; an automation run is skipped, not failed. Suspended sandboxes do not count. |
| `idleSuspendMinutes` | 60 | A conversation's sandbox is suspended: the pod goes, the volume stays. |
| `deleteSuspendedAfterDays` | 7 | A sandbox still suspended is deleted **with its volume**. |
| `automationOrphanMinutes` | 10 | An automation run's sandbox that never got a conversation is deleted. |
| `automationMaxMinutes` | 120 | An automation run's sandbox is deleted, whatever it is doing. |
| `maxEventsPerConversation` | 20000 | The oldest events of a transcript are dropped. |

Idle means no request or stream traffic through `/runtime/<sandbox>`, no event
from its agent, and no agent turn in progress — a long task keeps its sandbox
up with nobody watching. A suspended sandbox comes back with its checkout, its
agent's session and the conversation intact, and nothing is reinstalled.

Deletion after `deleteSuspendedAfterDays` is the one limit that loses work: a
branch that was never pushed goes with the volume. The transcript does not; the
conversation becomes an archived one.

A run that fails while its sandbox is still starting never releases it, which
is what `automationOrphanMinutes` is for.

## Metrics

The app server serves Prometheus text on `:9102/metrics`, scraped by the
existing kube-prometheus-stack through a ServiceMonitor. The figures are what
each sandbox's agent server reports for its conversation, read from every
running sandbox once a reconcile cycle and kept with the conversation, so they
outlive the sandbox and trail a turn by up to a minute.

| Metric | Labels |
| --- | --- |
| `openhands_conversation_cost_usd` | `conversation`, `title`, `trigger`, `model` |
| `openhands_conversation_tokens` | the same, and `kind` |
| `openhands_usage_cost_usd` | `model` |
| `openhands_usage_tokens` | `model`, `kind` |
| `openhands_conversations` | `status` |
| `openhands_conversations_total` | — |
| `openhands_sandboxes` | `spec`, `status` |

`kind` is `prompt`, `completion`, `cache_read`, `cache_write` or `reasoning`.
`model` is what the agent was asked for — an alias such as `sonnet` for a
Claude Code conversation — and the cost is the agent's own estimate, which for
a subscription login is not what is billed.

**Every series is a gauge**, including the ones that read like totals. They are
recomputed from the conversations that still exist, so deleting one makes the
numbers go down, which a counter may not do. Chart them with `max_over_time`,
not `rate`. A conversation whose sandbox was gone before usage was recorded
here has none.

The NetworkPolicy admits the monitoring namespace to that port and nothing
else: the scraper cannot reach the API.

`files/dashboards/openhands.json` is the Grafana dashboard ("OpenHands"),
shipped as a ConfigMap the Grafana sidecar loads: spend and tokens over time,
sandboxes and conversations by status, and a usage table per conversation.

## Storage

| Volume | Holds | Backed up |
| --- | --- | --- |
| `openhands-app-server` | the app server's database: conversations and their transcripts, settings, agent profiles, the encrypted secret store | yes |
| `openhands-data` | `~/.openhands` in the canvas pod: the automation service's database and uploads | yes |
| `workspace-<sandbox>` | one sandbox's checkout and agent state; deleted with the sandbox | no (`longhorn-tmp`: one replica, volume deleted with the PVC) |

All ReadWriteOnce Longhorn volumes, which is why both the app server and the
canvas pod are single replicas. The two backed-up volumes are in the Longhorn
recurring backup group. Sandbox volumes are not, and use `longhorn-tmp` so that
the volume goes with the Sandbox rather than lingering as a `Released` PV: a
checkout is a branch that is on the forge, or it is work in progress that the
limits above will eventually delete.

The secret store's key is not on a volume but in the
`openhands-app-server-secrets-key` Secret; without it the store cannot be read.

The automation service can be pointed at an external Postgres with
`AUTOMATION_DB_URL` (`postgresql+asyncpg://…`) if SQLite on one volume ever
stops being enough.

## Runbook

Find a conversation's sandbox, from the conversation id in the page's URL:

```sh
kubectl -n openhands exec deploy/openhands-app-server -c app-server -- python3 -c "
import sqlite3; print(sqlite3.connect('/data/app.db').execute(
  'select sandbox_id from conversations where id = ?', ('<conversation id>',)).fetchone())"
```

Every sandbox, with its spec and whether it is suspended:

```sh
kubectl -n openhands get sandbox -o custom-columns='NAME:.metadata.name,SPEC:.metadata.labels.openhands\.msng\.to/sandbox-spec,MODE:.spec.operatingMode,CREATED:.metadata.creationTimestamp'
```

A sandbox's logs — the pod is named after the sandbox:

```sh
kubectl -n openhands logs <sandbox> -c agent-server      # the agent server
kubectl -n openhands logs <sandbox> -c seed-home         # skills and the adapter install
kubectl -n openhands logs deploy/openhands-app-server | grep <sandbox>
```

Delete a wedged sandbox. Deleting the conversation in the UI is the normal
way; this is for when that does not work. The `Sandbox` owns its pod, Service,
volume and Secret, and the app server notices within a minute and shows the
conversation as archived:

```sh
kubectl -n openhands delete sandbox <sandbox>
```

A pod that will not go because its node is down needs the usual force-delete
and, for the volume, the stale VolumeAttachment removed.

Why a sandbox was suspended or deleted:

```sh
kubectl -n openhands logs deploy/openhands-app-server | grep 'collect:'
```

## Known rough edges

- **No editor tab.** The agent server's bundled editor listens on a second
  port the runtime proxy does not reach, so sandboxes start with it off.
- **No LLM profiles.** Conversations run ACP agents, which bring their own
  model; switching model is the ACP model switch, and an OpenHands-kind agent
  profile launches from `agent_settings`.
- **The Files tab does not follow an ACP agent's edits.** The frontend
  refreshes its file queries on `FileEditorObservation`-style events only; an
  ACP agent's edit arrives as an `ACPToolCallEvent` (`tool_kind: "edit"`), so
  an open file and the file list keep their old contents. The tab's refresh
  button reloads the open file but not the list, whose query key
  (`workspace-files-cloud`) is not the one it invalidates. Switching to
  another tab and back reloads both once the list is 30 s old. Same on
  canvas `1.25.0`.
- **The Terminal tab stays empty for ACP agents**, for the same reason: it
  is fed from `TerminalObservation` events, and an ACP shell call is an
  `ACPToolCallEvent` (`tool_kind: "execute"`).
- **Settings has no link to Secrets.** The page is at
  `/canvas/settings/secrets`; the sidebar's "All Cloud Settings" link points
  at a hosted settings UI this deployment does not have.
- **`readOnlyRootFilesystem` is false**, and not fixable by mounting more
  volumes: running shell commands anywhere on the filesystem is what the agent
  is for.
- **The cloud API is undocumented**, and the frontend changes it between
  releases. Every endpoint the app server implements is one the bundled
  frontend calls; on a canvas upgrade, start a conversation, send a message and
  read history back before trusting it.
