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

The canvas pod's own agent server is still started, for the automation
service's runs only; nothing routes a browser to it. The pod's entrypoint is
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

Sandbox egress is per spec (`sandboxSpecs`). The canvas pod's is DNS, the app
server, `443` on the MetalLB VIP pool (`git.msng.to`), and `443` to the internet
with RFC1918 excluded. Everything else, including the API server and every
other namespace, is denied.

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
over ACP — and from then on are owned by the Settings UI. The frontend's own
first-run wizard is a per-browser `localStorage` flag; close it once per
browser.

Codex and Gemini CLI ship as presets alongside Claude Code, and **Custom**
accepts the launch command of any stdio ACP server; credentials for them have
to reach the sandbox pod's environment, as `openhands-anthropic` does.

## Skills

`files/skills/` holds Agent Skills that the chart seeds into the agent's home on
every pod start, so git is the source of truth and a change takes effect on the
next rollout. Both the canvas and every sandbox profile get the same set.

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

## Sandbox profiles

Additional agent servers, each in its own pod with its own ServiceAccount,
NetworkPolicy, resource limits and credentials. Choosing a backend in the UI is
what chooses the isolation.

| Profile | Reaches | Forge credentials |
| --- | --- | --- |
| `repo` | the model API and `git.msng.to` | yes |
| `isolated` | the model API only — nothing on the LAN | no |

Each profile is served at `https://openhands.msng.to/sandbox/<name>`: a more
specific path on the canvas's own hostname, which Gateway API gives precedence
over the `/` route. Sharing the origin means one Authelia session covers
everything and there is no CORS to configure. Traefik authenticates the path
with a `forwardAuth` subrequest against the same oauth2-proxy, then strips the
prefix before the agent server sees the request — the agent server has no
concept of being served under one.

### Registering a profile in the UI

**The backend list is browser state, not server state.** It lives in
`localStorage` under `openhands-backends`, so nothing in this chart can
pre-register a profile and every browser and device has to be told once. Until
then the switcher shows only the built-in `Local` entry — id `default-local`,
pointing at this origin with the session key injected into the page — which is
the canvas pod itself, not a profile.

Open the backend switcher, choose **Add a backend**, and give it:

| Field | Value |
| --- | --- |
| Name | `repo` (anything; it is the label in the switcher) |
| Host | `https://openhands.msng.to/sandbox/repo` |
| API key | the profile's `session-api-key`, below |

```sh
kubectl -n openhands get secret openhands-sandbox-repo \
  -o jsonpath='{.data.session-api-key}' | base64 -d
```

Repeat for `isolated`. The session key is a second lock behind the Authelia
gate: reaching the path at all already requires a session, because Traefik runs
the `forwardAuth` subrequest before it proxies.

### Why profiles and not per-session pods

The plan this replaced assumed the isolation boundary could be per session. It
cannot: the agent server builds a `LocalWorkspace` and isolates a session with a
git worktree, and there is no configuration hook for a remote one. The SDK does
ship an `AgentSandboxWorkspace` that runs a session in a Kubernetes pod, but it
is a *client-side* class — something a Python program that drives an agent server
uses, not something an agent server can be pointed at.

So the pod boundary is the only isolation boundary available, and it has to be
something a session can be pointed at up front. A profile is that: sessions on
the same profile share its pod, sessions on different profiles share nothing —
not a ServiceAccount, not a volume, not a network path, not a forge token.

These are long-lived Deployments rather than `Sandbox` resources. A `Sandbox`
earns its keep when a pod is created and destroyed per unit of work; a backend
that has to keep a stable DNS name for the browser to reach is a Deployment.

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
| `/api/v1/app-conversations/{id}/{files,file,download,skills,git/*,switch_acp_model}` | browser | `X-Forwarded-User`; answered by the conversation's own agent server |
| `/api/automation/**` | browser | `X-Forwarded-User`; forwarded with the automation service's key |
| `:8081/sandboxes/{id}/{events,conversations}` | the sandbox's agent server | that sandbox's own session key |

`/runtime/{id}` strips the prefix and proxies HTTP and WebSockets to the
sandbox's agent server. It drops `Cookie`, `Authorization` and every
`X-Forwarded-*` header first: an agent server runs code the agent controls, and
the oauth2-proxy cookie must not reach it.

What the app server may do in the cluster is in
`templates/app-server-rbac.yaml`. The one grant worth reading twice is Secrets:
`create` only. It cannot read the model or forge credentials — sandboxes get
those by reference in the pod template — and it never reads back the keys it
minted, because it keeps them in its own database.

Starting a conversation is a start task the frontend polls: the app server
creates a sandbox from the default spec, waits for it to be `RUNNING`, then
starts the conversation on the sandbox's agent server with the stored
`agent_settings` and the chosen ACP agent profile laid over them. Titles are set
here from the first message — the agent server's own titling uses the agent's
LLM, which for an ACP agent is not one litellm can call.

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
neither the app server nor a browser ever handles them. Losing the key Secret
makes stored secrets undecryptable; they are re-entered, not restored.

The Settings UI renders itself from the agent server's settings schemas; an
initContainer asks the sandbox image's own agent server for them at pod start,
so they always match the version conversations run on.

A reconciler compares the database to the cluster every minute: a managed
`Sandbox` with no live row is deleted, and a row whose `Sandbox` has vanished
reports `MISSING`.

## Automations

Scheduled and event-driven runs live in the automation backend. They are rows in
the database on the data volume, created through the UI — this chart creates
none of them, and there is no declarative form for one.

Cron triggers work out of the box. Event triggers need a webhook source
registered first, because the deployment ships no built-in forge providers:
`GET /api/automation/v1/capabilities` reports `triggerKinds: ["cron"]` until one
exists.

To wire up Forgejo:

1. Register a custom webhook source in the automation service — **Automations →
   Webhooks** in the UI, or `POST /api/automation/v1/webhooks`:

   | Field | Value |
   | --- | --- |
   | `source` | `forgejo` |
   | `signature_header` | `X-Gitea-Signature` |
   | `signature_scheme` | `hmac_sha256_hex` |
   | `event_key_expr` | `action` |

   Forgejo signs the raw body with hex HMAC-SHA256 under that header, which is
   exactly what `hmac_sha256_hex` verifies. The response carries the generated
   secret and the delivery URL **once**.

2. Add a webhook on `ops/k8s-homelab` in Forgejo pointing at that URL, type
   `gitea`, with that secret, restricted to the events you want to act on.

3. `event_key_expr: action` means the event key is the payload's `action`
   (`created`, `opened`, `closed`), so restrict the Forgejo webhook to one event
   type rather than relying on the key to tell them apart.

`oauth2-proxy` already skips authentication for `POST` on the event path — a
webhook carries no Authelia session. The HMAC is what authenticates a delivery;
the path answers 404 for a source nobody has registered.

> A comment-triggered automation on a **public** repo means anyone with a
> Forgejo account can start a run. The HMAC proves the delivery came from
> Forgejo, not that the commenter is allowed to spend model budget. There is no
> author filter on a Forgejo webhook and no allowlist in the automation service,
> so the only place to enforce one is the automation's own prompt — which is a
> soft control. Prefer a schedule, or an event the bot account alone can raise.

## Metrics

`files/metrics/` is a sidecar that walks the conversation list and renders
per-model token and cost figures as Prometheus text on `:9102`, scraped by the
existing kube-prometheus-stack through a ServiceMonitor. The agent server
records the figures but exposes no `/metrics` endpoint of its own, and no
upstream usage dashboard exists.

It is a sidecar rather than its own Deployment because the session key it
authenticates with is generated onto the data volume on first boot — reading it
there is what keeps that key single-owned rather than copied into a Secret with
two owners. The volume is mounted read-only.

Series and their caveats are in `files/metrics/README.md`. The short version:
they are **gauges**, recomputed from the conversations that still exist, so
deleting a conversation makes the numbers go down.

The NetworkPolicy admits the monitoring namespace to the exporter's port and
nothing else — the scraper cannot reach the entry point, and oauth2-proxy
cannot reach the exporter.

## Storage

Two ReadWriteOnce Longhorn volumes, which is why this is a single replica:

| Volume | Mount | Backed up |
| --- | --- | --- |
| `openhands-data` | `~/.openhands` | yes |
| `openhands-workspace` | `~/workspace` | no |

`~/.openhands` holds settings, session history, the automation SQLite database,
and the **encryption key for the secret store, generated on first boot**. Losing
that volume does not just lose history — it makes every secret saved through the
UI undecryptable. It is in the Longhorn recurring backup group for that reason.

`~/workspace` is per-session checkouts of branches that are on the server
anyway, so it is not backed up.

The automation backend can be pointed at an external Postgres with
`AUTOMATION_DB_URL` (`postgresql+asyncpg://…`) if the SQLite database on a
single volume ever stops being enough.

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
- **No commit list in the Changes tab.** The frontend sends runtime calls it
  has no first-class endpoint for (`/api/git/commits`, bash event search,
  confirmation responses) through `POST /api/cloud-proxy`, which the app
  server does not implement. Uncommitted changes and their diffs work.
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
