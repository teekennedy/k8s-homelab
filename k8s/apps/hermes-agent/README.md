# hermes-agent

[Hermes Agent][upstream] as a personal assistant: one persistent agent with its
own memory, skills and schedules, talked to over Signal. It is separate from
[`k8s/apps/openhands`](../openhands), which runs disposable coding sessions;
the two share nothing.

[upstream]: https://github.com/NousResearch/hermes-agent

## Shape

```
  Signal ◄──────────► signal-cli ◄─ 127.0.0.1 ─► hermes gateway ──► model API (OpenRouter)
                      (same pod)                  │   agent loop, cron, memory, skills
  hermes.msng.to ──► Traefik ──► dashboard :9119 ─┤   signs in against Authelia itself
  (internal VIP)                                  │
                                                  └─ ssh :2222 ──► sandbox pod (StatefulSet)
                                                                   sshd as `agent`, /home/agent volume

  iPhone app ──► hermes-relay.msng.to ──► relay pod ◄── WebSocket ── connector sidecar
                 (public VIP, CrowdSec)   (StatefulSet, SQLite)      in the gateway pod
```

**The gateway pod** (`templates/gateway.yaml`) is the agent. One `hermes
gateway run` process holds the Signal connection, runs the agent loop for each
message, ticks cron jobs every minute and serves the dashboard. Everything it
remembers is on the `data` volume (`/opt/data`): sessions in SQLite, memories,
skills it writes for itself, cron jobs. That volume has one writer, so the
StatefulSet has one replica.

**The sandbox pod** (`templates/sandbox.yaml`) is where the agent's terminal,
file and code-execution tools run. Hermes has no Kubernetes backend, so it
uses its `ssh` one against a one-replica StatefulSet that runs nothing but a
non-root sshd. It is not an agent-sandbox `Sandbox`: that controller never
replaces a running pod when its template changes (upstream
kubernetes-sigs/agent-sandbox#581), so an image bump would never reach this
long-lived pod.
The point of the split is what is *not* in that pod: the model key, the Signal
account and the agent's memory all stay in the gateway, so a command the model
was talked into running cannot read them. Web tools still run in the gateway
process; the browser runs in the sandbox (see "Sandbox image").

**Files the agent makes reach the gateway through the sandbox's home volume**,
which the gateway mounts read-only at the same path, `/home/agent`. Hermes
sends a reply's `MEDIA:<path>` attachments, and hands images to vision and
subagents, from the gateway's own filesystem; without the mount every sandbox
path was "not found on this host" and dropped without a word to the user.
Hermes' own fallback, fetching the file over the SSH backend, finds the
connection already closed at the end of the turn. The volume is
ReadWriteOnce, so the gateway is scheduled onto the sandbox's node (see
"Known rough edges").

A turn, end to end: a Signal message arrives over signal-cli's event stream;
the gateway loads the session, calls the model, runs the tool calls it asks
for (shell and files over SSH, the rest in-process), and repeats until the
model answers in text; the answer goes back over Signal. Afterwards a
background review may save a memory or write a skill.

## Configuration

Hermes keeps `config.yaml` on the data volume and rewrites it itself (the
dashboard, `hermes config set`, version migrations). Git owns only the keys
that matter to this deployment, through Hermes' *managed scope*: the
`hermes-agent-managed` ConfigMap is mounted at `/etc/hermes-managed`, each key
in it wins over the data volume's, and Hermes refuses to change it ("managed
by your administrator"). Those keys are the model, the terminal backend, the
approval mode, the timezone and the dashboard's sign-in; add more under
`config` in `values.yaml`. Everything else belongs to the agent.

Secrets reach Hermes as environment variables only. `/opt/data/.env` is
loaded *over* the environment, so a key written there (by the dashboard, say)
shadows the Secret's.

### Model

`model` in `values.yaml`: a Hermes provider id, the main model and a cheap
auxiliary model for context compression, command-risk checks, session
titles and vision (`vision_analyze`, image inspection by subagents), so it
must accept images. Other auxiliary tasks use the main model. The default
is OpenRouter with `z-ai/glm-5.3` and `deepseek/deepseek-v4.1-flash`. Hermes
needs tool calling and at least 64k of context from whatever replaces them.
For another provider, set `model.provider` and put that provider's key
variable in `hermes-agent-env`.

### Approvals

`approvals.mode: smart`: the auxiliary model judges each command, and Hermes
asks in the chat before running one it considers dangerous. Cron jobs have
no one to ask and are denied those commands.

## Prerequisites

### Secrets you create by hand

| Secret | Keys |
| --- | --- |
| `hermes-agent-env` | `OPENROUTER_API_KEY`, `SIGNAL_ACCOUNT`, `SIGNAL_ALLOWED_USERS` |

```sh
kubectl -n hermes-agent create secret generic hermes-agent-env \
  --from-literal=OPENROUTER_API_KEY=sk-or-... \
  --from-literal=SIGNAL_ACCOUNT=+15551234567 \
  --from-literal=SIGNAL_ALLOWED_USERS=+15557654321
```

`SIGNAL_ACCOUNT` is the number the agent answers on, `SIGNAL_ALLOWED_USERS`
the comma-separated senders it answers to. Without the second, a stranger who
messages the agent gets a pairing code rather than an answer.

A sender whose Signal account hides its phone number ("Who can see my
number: Nobody") arrives as its account UUID, not its number, and is dropped
until that UUID is in `SIGNAL_ALLOWED_USERS` too. The drop is logged to
`/opt/data/logs/agent.log` (not the pod log) with the UUID to add:

```sh
kubectl -n hermes-agent exec hermes-agent-0 -c hermes -- \
  grep 'Unauthorized user' /opt/data/logs/agent.log
``` The Secret is
optional to the pod, which starts without it and has no model; restart the
pod after changing it.

### Secrets provisioned for you

All by mittwald secret-generator: `hermes-agent-oidc` (reflected to
auth-system and hashed into Authelia by its PreSync job),
`hermes-agent-api-server`, the two SSH key pairs, `hermes-agent-ssh-client`
and `hermes-agent-ssh-host`, and the iOS relay's keys, `hermes-agent-relay`.

### Authelia

Wired in `k8s/platform/auth-system/values.yaml`: the `hermes-agent` OIDC
client and authorization policy, the `oidcClientSecrets.clients` entry and the
projected key, and the `hermes-admins` LLDAP group.

### Signal

signal-cli needs an account before Hermes can use it. Either way, the pod must
be restarted afterwards for the daemon to load the account.

**A number of its own** (recommended: the agent is a separate contact). With
a number that can receive one SMS or call:

```sh
kubectl -n hermes-agent exec -it hermes-agent-0 -c signal-cli -- \
  signal-cli --config /var/lib/signal-cli -a +15551234567 register
# Signal usually demands a captcha first: solve
# https://signalcaptchas.org/registration/generate.html, copy the
# signalcaptcha:// link, and repeat with `register --captcha 'signalcaptcha://...'`.
kubectl -n hermes-agent exec -it hermes-agent-0 -c signal-cli -- \
  signal-cli --config /var/lib/signal-cli -a +15551234567 verify 123456
kubectl -n hermes-agent delete pod hermes-agent-0
```

**Linked to your own number.** The agent becomes a linked device of your
account and you talk to it in *Note to Self*. It receives everything your
account does; `SIGNAL_ALLOWED_USERS` (your own number) decides what it acts on.

```sh
kubectl -n hermes-agent exec -it hermes-agent-0 -c signal-cli -- \
  signal-cli --config /var/lib/signal-cli link -n Hermes
# Prints an sgnl:// URI and waits. Render it (`qrencode -t ansiutf8 'sgnl://...'`)
# and scan it in Signal: Settings → Linked devices.
kubectl -n hermes-agent delete pod hermes-agent-0
```

## Security posture

`hermes.msng.to` is on the internal gateway only. The dashboard can edit the
agent's configuration and credentials and has a chat tab that is a terminal in
the gateway pod, so **anyone who can sign in owns the agent**. Hermes' OIDC
support has no user or group allowlist; the access control is the Authelia
client's authorization policy (`hermes-admins` and `full-admin`, two factor).

| Pod | In | Out |
| --- | --- | --- |
| gateway | dashboard port, from Traefik | internet (80/443), the sandbox's sshd, the gateway VIPs (OIDC issuer; the iOS relay, by its public name) |
| sandbox | sshd, from the gateway | internet (80/443) |
| relay | `/v1`, from Traefik's public entrypoint | internet (80/443): APNs |

Neither pod has a ServiceAccount token. The sandbox runs as uid 10001 with a
read-only root filesystem and no capabilities. The gateway's `hermes`
container starts as root, because the image's init (s6-overlay) fixes
ownership of the data volume before dropping every service to uid 10000; its
capabilities are cut to the ones that takes.

The gateway reads the sandbox's home (read-only, with the sandbox's group as
a supplementary group); nothing of the gateway's is visible to the sandbox.

The sandbox's host key and the gateway's client key are generated Secrets.
The gateway pins the host key in `known_hosts` at every start, and the sandbox
mounts only the public half of the client key.

The gateway reaching the gateway VIPs means the agent's web tools could reach
other internal sites by name. Hermes refuses private addresses in those tools
by default (`security.allow_private_urls`); leave that off.

## Sandbox image

`files/sandbox/Dockerfile` is Debian with sshd, Python (with numpy), Node,
ffmpeg, git and a few CLI tools, running as `agent` (uid 10001). Hermes needs
`bash`, `tar` and `python3` on the far side, and an SFTP subsystem for `scp`.

It also carries Hermes' *Bot Desktop*: TigerVNC's `Xvnc`, the Xfce pieces and
Debian's Chromium. With the `ssh` backend Hermes puts the desktop, and the
browser on it, in the sandbox (`bot_desktop.placement: auto`). Over SSH it
starts everything as `agent`, copies its launcher into `/tmp` and relays CDP
and VNC through `python3` on stdio, so sshd needs no forwarding. The package
list mirrors upstream's `docker/sandbox-desktop.Dockerfile`, minus
`cua-driver` (`computer_use`). Upstream's prebuilt
`nousresearch/hermes-sandbox:desktop` would replace this image with a root
default user, passwordless `sudo` and no sshd. Hermes only probes for a
Playwright Chromium (`/opt/playwright/chromium-*/chrome-linux*/chrome`), so
Debian's binary is symlinked there and gets Debian's security updates.

A conventional commit on `main` that touches `files/sandbox/` is tagged
`hermes-sandbox-<version>` and published as
`ghcr.io/teekennedy/k8s-homelab:hermes-sandbox-<version>`; Renovate then
proposes the new tag for `sandbox.image`. Anything the agent installs outside
`/home/agent` is gone at the next restart: add it to the image instead.

## iOS app

The [Hermes iOS][hermes-ios] app talks to Hermes through two pieces, both
run by this chart. The *relay* is the one endpoint the phone and the
connector both reach; it runs nothing itself (`HERMES_ADAPTER=connector`).
The *connector* holds a WebSocket open to the relay and runs each message
from the phone as a Hermes job.

[hermes-ios]: https://github.com/dylan-buck/Hermes-iOS

```
  iPhone ──► hermes-relay.msng.to ──► relay pod ◄── WebSocket ──┐ (out through Traefik)
                                                                 │
  gateway pod:  hermes-mobile ──► 127.0.0.1:8642 ──► hermes (API server: agent loop)
                (connector)                            └─ spawns hermes-mobile-mcp
                    └── /opt/data/hermes-mobile: credential, sensors.db ──┘
```

**The relay** is public at `hermes-relay.msng.to`, on the public listener,
with only `/v1` routed. That is what the namespace's `external-gateway-access`
label is for; the dashboard's route stays on the internal listener. The relay's
own auth guards it: a connector must present `CONNECTOR_SETUP_SECRET` to
register, a phone pairs with a short-lived code the connector issues, and
both then hold relay-issued tokens. It keeps its state in SQLite on its own
volume, and is one replica: rate limits and connector sockets live in the
process.

**The connector** is the `hermes-mobile` container in the gateway pod. It
cannot be a pod of its own: its setup refuses to register without a runnable
`hermes` CLI, and the phone's location and health tools are an MCP server,
`hermes-mobile-mcp`, that Hermes spawns as a subprocess and that reads the
connector's `sensors.db`. So it runs on the Hermes image, as the hermes user,
with the connector mounted read-only from the relay image (an image volume
at `/opt/hermes-mobile`, mounted in the `hermes` container too). It keeps its
state on the data volume at `/opt/data/hermes-mobile`; Hermes' agent can read
it, as it can the rest of the gateway's secrets, and the sandbox cannot. On
first start it registers with the relay using the setup secret, retrying
until the relay answers. Jobs run on Hermes' API server over localhost, so
the phone's conversations get the same memory, skills and model as Signal.
The managed config pins the MCP server (`mcp_servers.hermes_mobile`), and each
start copies upstream's `hermes-ios` skill into `/opt/data/skills`.

**The image** is built from `files/relay/Dockerfile`, which fetches upstream's
`relay/`, `connector/` and `skills/` at a pinned commit and installs them from
upstream's `uv.lock` files under `/opt/hermes-mobile`. Upstream publishes no
image. Moving to a newer upstream commit is a hand edit of that `ADD` line;
the commit releases `hermes-relay-<version>` like the sandbox image, and
Renovate proposes the tag, which both the relay and the connector use.

**Check server status**:

```sh
kubectl -n hermes-agent exec hermes-agent-0 -c hermes-mobile -- hermes-mobile status
```

**Pairing a phone.**:

```sh
kubectl -n hermes-agent exec -it hermes-agent-0 -c hermes-mobile -- \
  hermes-mobile pair-phone
```

This will print a QR code, valid for 10 minutes. Open the app and scan the QR
code to pair.

**Voice mode** (OpenAI Realtime) is optional and keyed on the connector, not
the app. It stores the key in the connector's state:

```sh
kubectl -n hermes-agent exec -it hermes-agent-0 -c hermes-mobile -- \
  hermes-mobile configure-realtime
```

**Push notifications** are optional. To enable them, put the APNs settings
(`APNS_KEY_CONTENTS`, `APNS_KEY_ID`, `APNS_TEAM_ID`, `APNS_BUNDLE_ID`,
`APNS_ENVIRONMENT`) in a `hermes-agent-relay-env` Secret and restart the
relay. Every key in it becomes an environment variable.

**Starting over** (a new relay database, a rotated setup secret or API server
key, all of which the connector's state records):

```sh
kubectl -n hermes-agent exec hermes-agent-0 -c hermes-mobile -- \
  rm /opt/data/hermes-mobile/state.json
kubectl -n hermes-agent delete pod hermes-agent-0   # registers again; re-pair phones
```

## Storage

| Volume | Holds | Backed up |
| --- | --- | --- |
| `data-hermes-agent-0` | Hermes' home: sessions, memories, skills, cron | yes |
| `signal-hermes-agent-0` | the Signal account's keys | yes |
| `home-hermes-agent-sandbox-0` | `/home/agent`: the agent's working files; read-only in the gateway too | yes |
| `data-hermes-agent-relay-0` | the iOS relay's SQLite: paired phones and hosts, message jobs | yes |

## Runbook

```sh
# Is the gateway up, and is Signal connected?
kubectl -n hermes-agent logs hermes-agent-0 -c hermes --tail=100
kubectl -n hermes-agent logs hermes-agent-0 -c signal-cli --tail=50

# The hermes CLI. It drops from root to the hermes user by itself.
kubectl -n hermes-agent exec -it hermes-agent-0 -c hermes -- hermes doctor

# Approve a pairing code a new Signal contact was sent.
kubectl -n hermes-agent exec -it hermes-agent-0 -c hermes -- \
  hermes pairing approve signal <CODE>

# Can the gateway reach the sandbox?
kubectl -n hermes-agent exec hermes-agent-0 -c hermes -- \
  /command/s6-setuidgid hermes ssh -p 2222 -i /opt/data/.ssh/id_rsa -o BatchMode=yes \
  agent@hermes-agent-sandbox.hermes-agent.svc.cluster.local id
```

Rotating an SSH key pair: delete its Secret, let Argo CD and secret-generator
recreate it, then restart both pods.

## Known rough edges

- **The namespace predates its `external-gateway-access` label**, and Argo CD
  never labels a namespace it did not create. Until it is labeled by hand
  (`kubectl label ns hermes-agent external-gateway-access=true`), the relay's
  route is not admitted and `hermes-relay.msng.to` does not serve.

- **The gateway follows the sandbox's node.** Both pods mount the sandbox's
  ReadWriteOnce home, which attaches to one node. The gateway *requires* the
  sandbox's node; the sandbox only *prefers* the gateway's, so that with both
  pods gone (a drain, a fresh install) the sandbox schedules anywhere and the
  gateway follows. If the sandbox is replaced alone and its preferred node is
  full, it lands elsewhere and sticks in `ContainerCreating` on a multi-attach
  error: delete the gateway pod and it follows. With no sandbox pod at all,
  the gateway stays `Pending`.

- **The `hermes` container's capability list** is the minimum the image's
  init (v0.23.2) needs to fix ownership and drop to uid 10000. If an image
  bump makes init fail on a permission error, add the capability it names,
  or drop the `capabilities` block to confirm.
- **signal-cli's daemon runs with no account** on its volume, which is what
  lets the account be registered or linked in the running pod.
- **No metrics.** Hermes exposes no Prometheus endpoint; `/health` on the API
  port is what the probes use, and the pod's restarts are the only alert
  signal.
- **Upstream moves fast**: roughly weekly releases with config migrations.
  The image is pinned by digest; read the release notes with each Renovate
  bump.
- **`kubectl exec` runs as root** in the `hermes` container. The `hermes`
  command on PATH is an upstream shim that drops to the hermes user itself;
  wrap anything else that writes to `/opt/data` in
  `/command/s6-setuidgid hermes` (not on exec's PATH) so it leaves no
  root-owned files on the data volume.
- **Warnings that are not faults.** `install out of sync (pnpm: ...)` at
  startup: the upstream runtime image ships no pnpm on purpose and uses a
  prebuilt TUI bundle. `Auxiliary Nous client unavailable ... (run: hermes
  auth)` at startup and on each turn: the vision-backend probe tries every
  provider, Nous Portal included; OpenRouter answers, so no `hermes auth` is
  needed.
- **`/opt/data/logs/agent.log` has more than the pod log**: dropped senders,
  model errors (a `402` from OpenRouter is an unfunded key) and per-turn
  detail. Look there when a message gets no answer.
- **`terminal.cwd` is `.`, not `/home/agent`.** For the SSH backend that
  leaves the gateway's `TERMINAL_CWD` unset and the sandbox shell starts in
  `~` (`/home/agent`). A literal path is also checked on the gateway's own
  filesystem, which sees the sandbox's home only read-only.
- **`HERMES_WRITE_SAFE_ROOT` is `/opt/data:/home/agent`.** The image's
  default, `/opt/data`, is checked against the path alone, so with the SSH
  backend it refused every file-tool write into the sandbox ("is outside
  HERMES_WRITE_SAFE_ROOT"). Writes elsewhere in the sandbox, `/tmp` included,
  are still refused; the agent can use the terminal for those.
- **A 503 on `/auth/login` is the dashboard failing OIDC discovery**, shown
  as Traefik's standby page. `curl` the login URL inside the pod for the
  real error.
