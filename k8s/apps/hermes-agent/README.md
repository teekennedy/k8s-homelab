# hermes-agent

[Hermes Agent][upstream] as a personal assistant: one persistent agent with its
own memory, skills and schedules, talked to over Signal. It is separate from
[`k8s/apps/openhands`](../openhands), which runs disposable coding sessions;
the two share nothing but the agent-sandbox controller.

[upstream]: https://github.com/NousResearch/hermes-agent

## Shape

```
  Signal ◄──────────► signal-cli ◄─ 127.0.0.1 ─► hermes gateway ──► model API (OpenRouter)
                      (same pod)                  │   agent loop, cron, memory, skills
  hermes.msng.to ──► Traefik ──► dashboard :9119 ─┤   signs in against Authelia itself
  (internal VIP)                                  │
                                                  └─ ssh :2222 ──► Sandbox pod (agents.x-k8s.io)
                                                                   sshd as `agent`, /home/agent volume
```

**The gateway pod** (`templates/gateway.yaml`) is the agent. One `hermes
gateway run` process holds the Signal connection, runs the agent loop for each
message, ticks cron jobs every minute and serves the dashboard. Everything it
remembers is on the `data` volume (`/opt/data`): sessions in SQLite, memories,
skills it writes for itself, cron jobs. That volume has one writer, so the
StatefulSet has one replica.

**The sandbox pod** (`templates/sandbox.yaml`) is where the agent's terminal,
file and code-execution tools run. Hermes has no Kubernetes backend, so it
uses its `ssh` one against a `Sandbox` that runs nothing but a non-root sshd.
The point of the split is what is *not* in that pod: the model key, the Signal
account and the agent's memory all stay in the gateway, so a command the model
was talked into running cannot read them. Web and browser tools still run in
the gateway process.

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
auxiliary model for context compression, command-risk checks and session
titles. Other auxiliary tasks use the main model. The default
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
`hermes-agent-api-server`, and the two SSH key pairs, `hermes-agent-ssh-client`
and `hermes-agent-ssh-host`.

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
| gateway | dashboard port, from Traefik | internet (80/443), the sandbox's sshd, the gateway VIPs (OIDC issuer) |
| sandbox | sshd, from the gateway | internet (80/443) |

Neither pod has a ServiceAccount token. The sandbox runs as uid 10001 with a
read-only root filesystem and no capabilities. The gateway's `hermes`
container starts as root, because the image's init (s6-overlay) fixes
ownership of the data volume before dropping every service to uid 10000; its
capabilities are cut to the ones that takes.

The sandbox's host key and the gateway's client key are generated Secrets.
The gateway pins the host key in `known_hosts` at every start, and the sandbox
mounts only the public half of the client key.

The gateway reaching the gateway VIPs means the agent's web tools could reach
other internal sites by name. Hermes refuses private addresses in those tools
by default (`security.allow_private_urls`); leave that off.

## Sandbox image

`files/sandbox/Dockerfile` is Debian with sshd, Python, git and a few CLI
tools, running as `agent` (uid 10001). Hermes needs `bash`, `tar` and
`python3` on the far side, and an SFTP subsystem for `scp`.

A conventional commit on `main` that touches `files/sandbox/` is tagged
`hermes-sandbox-<version>` and published as
`ghcr.io/teekennedy/k8s-homelab:hermes-sandbox-<version>`; Renovate then
proposes the new tag for `sandbox.image`. Anything the agent installs outside
`/home/agent` is gone at the next restart: add it to the image instead.

## Storage

| Volume | Holds | Backed up |
| --- | --- | --- |
| `data-hermes-agent-0` | Hermes' home: sessions, memories, skills, cron | yes |
| `signal-hermes-agent-0` | the Signal account's keys | yes |
| the Sandbox's `home` claim | `/home/agent`: the agent's working files | yes |

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
  filesystem, where it does not exist, and logs a warning on every lookup.
- **`HERMES_WRITE_SAFE_ROOT` is `/opt/data:/home/agent`.** The image's
  default, `/opt/data`, is checked against the path alone, so with the SSH
  backend it refused every file-tool write into the sandbox ("is outside
  HERMES_WRITE_SAFE_ROOT"). Writes elsewhere in the sandbox, `/tmp` included,
  are still refused; the agent can use the terminal for those.
- **A 503 on `/auth/login` is the dashboard failing OIDC discovery**, shown
  as Traefik's standby page. `curl` the login URL inside the pod for the
  real error.
