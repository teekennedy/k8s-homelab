# nix-builders

The module's Nix and devenv builds can run on remote Nix builders over
`ssh-ng`, authenticated through a forwarded SSH agent. When the builders can't
be used, builds run in the Dagger engine's container as they always have.

| Function | What it does |
| --- | --- |
| `status` | Probe every configured builder and say which ones a build would use |

Builds that use the builders: `nix/validate`, `lab/build`, `lab binary-nix`,
`lab/update-vendor-hash`, and every devenv shell built from source (the ci
toolchain when no published image applies, `devenv toolchain`,
`devenv container`, and so the devenv releases). `nix/validate-build-metrics`
never uses them: its assertions need a cold, local store.

## Settings

Builders are constructor settings, so they apply to `dagger check` and
`dagger api call` alike.

| Setting | Value |
| --- | --- |
| `nixBuilders` | Machines in Nix's `builders` format, one per line or per `;`. Only `ssh-ng://` and `ssh://` machines are used, and a machine's SSH key field is ignored |
| `sshAuthSock` | The agent, as `unix:///path/to/socket` |
| `sshConfig` | Optional ssh_config, such as `~/.ssh/config`, for host names, users and ports |
| `sshUser` | Optional login, for machines whose URI and ssh_config name none (ssh's default, the local username, would be `root` in the container) |
| `sshKnownHosts` | The builders' host keys. Defaults to `k8s/platform/woodpecker/files/known_hosts`, which lists every borg host by name, FQDN and IP |

### On a workstation

Store them in your user-level Dagger config (`-g`), not in `dagger.toml`:

```sh
dagger settings -g homelab nixBuilders "$(cat /etc/nix/machines)"
dagger settings -g homelab sshAuthSock "unix://$HOME/.ssh/agents/s.user"
dagger settings -g homelab sshConfig "$HOME/.ssh/config"
dagger settings -g homelab sshUser "$USER"
dagger api call nix-builders status
```

Give `sshAuthSock` a path that stays the same across logins: on macOS
`$SSH_AUTH_SOCK` changes with every boot, and the agent's path is part of
the cache key of every build that uses it. A changed path doesn't break
anything; it reruns those builds once.

Remove them with `dagger settings -g -u homelab <setting>`.

### In CI and Renovate

`dagger.toml`'s `ci` env sets the borg hosts as the `nix-ci` login, with the
agent at `/tmp/nix-ci-agent.sock`. The PR checks and the release pipeline
source `scripts/nix-ci-ssh-agent.sh`, which starts that agent with the
short-lived `nix-ci` certificate, then run `dagger --env ci ...`. If the
certificate can't be read, the agent has nothing and every build runs in the
engine.

Renovate's post-upgrade tasks get the same. Its `dagger` wrapper
(`k8s/platform/renovate/files/dagger.sh`) starts that agent itself and adds
`--env ci`, for the repos in `NIX_CI_REPOSITORIES` (the renovate chart's
`customEnvVariables`) whose `dagger.toml` has a `ci` env. The wrapper does it
rather than a script in the repo because it runs with the pod's credentials
for every repo Renovate discovers. The `renovate` service account may read the
certificate's secret in `woodpecker`, and nothing else there.

## What happens in a build

1. **Match the engine.** Every build targets the engine's platform, so only
   machines that list its Nix system are kept: on an Apple-silicon engine
   (`aarch64-linux`), x86_64-linux builders are skipped. With none left the
   build is exactly what it is without builders, cache keys included, so the
   settings can stay on while you switch between engines. A machine that
   lists no system is kept, as Nix treats it as the client's.
2. **Probe.** A small container asks every machine's daemon for its store
   info over ssh, in parallel, and records whether it answered and whether
   it trusts the caller. Builds within the same five-minute window share one
   probe.
3. **Build.** If any machine answered, the build runs with
   `builders = @/etc/homelab-nix/machines`, `builders-use-substitutes = true`,
   and the agent and an ssh_config mounted. Images without an ssh client
   (devenv's) get the Nix image's, copied into their store from a binary
   cache.
4. **Fallback.** If the build fails with something only a missing builder
   explains ("cannot build on", "failed to start SSH", a rejected signature,
   ...; `remoteNixFailures` in `nix_builders.go`), it runs again in the
   container, without builders. Any other failure, such as a compile error,
   is reported as it is, without a second attempt.

No key reaches the engine. Only the agent socket is forwarded, and the ssh
config the build uses overrides `IdentitiesOnly`, `StrictHostKeyChecking`,
`UserKnownHostsFile` and the connection-sharing options from `sshConfig`,
since the key files and control sockets it names exist only on the host. A directory such as `~/.ssh` is never passed: Dagger applies
`+ignore` filters on the engine after the upload, so private keys in it would
reach the engine's cache.

### Trusted and untrusted builders

How the container builds depends on whether every builder that answered
trusts it, which the probe reads from the daemon.

| | Trusted (your login, a `trusted-users` member) | Untrusted (`nix-ci`) |
| --- | --- | --- |
| Builds in the container | Yes, alongside the builders | No: `max-jobs = 0` |
| Store volume | The usual one | A separate, signed-only one |

An untrusted client can only hand a builder inputs that are signed by a key
the builder trusts or that are content-addressed. Paths built in the
container are neither, so with an untrusted builder nothing is built there,
and that store is kept apart from the usual one, which holds such paths. The
signed-only store then only ever holds paths from cache.nixos.org or the
builders, which sign everything they build with the cluster key.

### Connection sharing

Nix hands each derivation to a builder from a `nix __build-remote` process of
its own, and a daemon worker looking paths up in its peers is a process of its
own too. Each opens a new ssh connection unless a master is running: Nix only
starts one itself for a store with more than one connection (`ssh-ng`
defaults to one), and otherwise uses one an ssh_config provides, which it
finds with `ssh -O check`.

So both sides share connections. The module's ssh config sets `ControlMaster
auto` with a socket in the container's `/tmp`; six small builds on one
builder took 3.2s with it and 8.9s without. On the hosts,
`nix/modules/builders` does the same for root's connections to its peers,
with sockets in `/run/nix-builders-ssh`, so the substituter lookups a builder
makes for each derivation it's handed reuse one connection per peer.

The same applies to a workstation. Builds there run under nix-daemon as
root, whose ssh reads `/etc/ssh/ssh_config` (on nix-darwin,
`/etc/ssh/ssh_config.d/100-nix-darwin.conf`) and root's own config, never
your `~/.ssh/config`. Put the connection-sharing options in the `Host borg-*`
block there, with a ControlPath root can write, such as
`/var/run/nix-ssh-%C`.

### Why the builders aren't substituters too

Making each builder an `ssh-ng` substituter as well made evaluating the lab
CLI's flake take about 10 seconds instead of 1, since Nix asked every builder
about every path. Asked to build a derivation it already has, a builder hands
back the output instead, so nothing is lost.

## When builders help

Measured on 2026-10-21, with the engine on borg-3 (i7-9750H, 12 threads) and
a one-line change to the lab CLI:

| | In the container | On borg-1 or borg-2 |
| --- | --- | --- |
| Evaluation | 1.4s | 1.2s |
| Handing the build to the builder | — | ~7s |
| `buildPhase` | 28s | 49–51s |
| Total | 50s | 1m40s |

One large derivation runs slower remotely. The per-derivation handoff is
mostly the builder looking up the new paths in its own substituters,
including an ssh connection to each of its peers, which the hosts' connection
sharing (above) is meant to cut; these numbers predate it. A native rebuild on borg-2
takes 48s too, so the compile time is the host, not remote building.

Builders pay off when a build has many derivations that can run in parallel
across hosts, or when a builder already has the outputs, and they take the
work off the engine's node. Nix gives a free slot to a builder before
building locally, and picks among builders by speed factor and load, so the
machines' job counts and speed factors decide where builds land.

## Hosts

`nix/modules/builders` gives every builder a `nix-ci` system user that is not
in `trusted-users`. Its daemon builds only in the sandbox, and it can't place
arbitrary contents at a store path the hosts might later use, as a trusted
user could. That matters because the PR checks run code from the pull
request.

`nix-ci` has no `authorized_keys`. sshd accepts an OpenSSH certificate for
principal `nix-ci` signed by the cluster's SSH user CA (the one the deploybot
login trusts; see [nixos-cd](../../docs/nixos-cd.md)). The principal line
carries `restrict,command="/run/current-system/sw/bin/nix-daemon --stdio"`,
so the certificate is good for the daemon protocol and nothing else: no
shell, pty or forwarding.

The certificate is minted by the `deploybot-ssh-cert-issuer` CronJob in
cert-system (`deploybotSshCa.extraCerts`), with a fresh key every six hours
and a 12-hour validity, into the `nix-ci-ssh-cert` secret in `woodpecker`,
which only the `dagger-pipeline` service account can read.

### Rollout

1. Sync woodpecker, whose RBAC lets the issuer write `nix-ci-ssh-cert` and the
   pipelines read it, and renovate, whose RBAC lets it read the secret too;
   then cert-system. The issuer runs every six hours; to
   issue now:
   `kubectl -n cert-system create job --from=cronjob/deploybot-ssh-cert-issuer deploybot-ssh-cert-issuer-manual`.
2. Deploy the hosts (the self-update pipeline does it on merge).
3. A PR's `dagger --env ci check` logs which builders it used; or, from a
   Woodpecker step, `dagger --env ci api call nix-builders status`.

Until the hosts have the `nix-ci` user, the probe is refused and CI builds in
the engine, exactly as before.
