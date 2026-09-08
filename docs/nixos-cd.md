# NixOS continuous deployment

Pushes to `main` that touch a NixOS configuration roll out automatically. CI
only triggers the rollout; each host fetches, builds, and activates its own
updated derivation, so CI never needs privileged access to any host.

## Overview

```
push to main (flake.nix, flake.lock, nix/**)
  │
  ├─ .woodpecker/deploy-hosts.yaml
  │    step "stage":   ssh deploybot@host "deploy <sha>"    (all hosts in parallel)
  │    step "rollout": kubectl create job --from=cronjob/nixos-rollout
  │
  └─ nixos-rollout Job (k8s/platform/woodpecker)
       for each cluster NixOS host:
         ssh deploybot@host "reboot"    → sentinel, or UP_TO_DATE
         wait for the host to come back on its staged generation and go Ready
```

The host itself handles building and activating the new NixOS generation.
The systemd unit `nixos-selfupdate.service` clones the repo, checks out the
deployment sha set by Woodpecker (defaults to main) builds this host's
`nixosConfigurations` attribute, and runs `nixos-rebuild boot` to activate it
on the next boot. CI never builds anything and never supplies any code.

## Design

**CI built with least privilege.** CI logs in as `deploybot`, an unprivileged
account whose sshd `Match` block only allows a handful of specific commands.
Privileged operations go through literal, argument-for-argument sudoers entries
with no wildcards. The only input that CI has control over is the commit sha.

**The commit must already be on `main`.** `nixos-selfupdate.service` rejects
anything that is not a full sha *and* an ancestor of the tracked branch. The
worst a compromised Woodpecker can do is ask a host to re-deploy an older commit
that was already on `main`.

**A host never moves backwards.** Being on `main` does not make a commit newer
than what the host already built, and requests do not arrive in commit order:
Renovate lands nix-touching commits in bursts, pipelines for them overlap, get
superseded, and get restarted by hand days later. So `nixos-selfupdate.service`
refuses any commit that is an ancestor of the one it last built. Without it the
last request to arrive wins, and a fleet ends up staging a commit older than the
one it is running. Deliberate rollback still works, it just has to be
deliberate — remove `/var/cache/nixos-selfupdate/last-rev` first.

**A pipeline can only report its own build.** `systemctl start --wait` on a unit
that is already running *joins* the running job rather than queueing a new one:
it returns when that run finishes, and that run read its commit before this one
was written. The trigger would then print a build of a commit nobody asked it
for and exit 0. The unit consumes `TARGET_REV_FILE` when it reads it, so the
file still existing afterwards is the signal that the run belonged to somebody
else; the trigger starts the unit again, up to `MAX_ATTEMPTS` times, and fails
rather than reporting a stranger's success.

**Hosts stage in parallel, reboot in order.** Staging order carries no meaning —
the hosts build independently and none of them reboots during the stage step —
so the fleet is staged all at once and the step lasts as long as the slowest
single host rather than the sum of four. Sequentially it exceeded the pipeline
timeout on a nixpkgs bump and was killed partway through the fleet. Only reboot
order matters, and that belongs to the rollout Job.

**The hosts are binary caches for each other.** They share a nixpkgs pin and
most of a system closure, so the same paths were being built or downloaded four
times over. `nix.builders.substituteFromClusters` (see `nix/modules/builders`)
points each host at its three peers as substituters, ahead of cache.nixos.org
because the transfer is local. This is *not* the setting that caused the build
delegation ring: `remoteClusters`, which hands builds to peers, stays empty. A
substituter can only answer for paths it already has, so a miss falls through to
a local build rather than to another hop.

Staging in parallel means the hosts race and mostly miss each other's caches
anyway. Staging one host first and then the rest in parallel would collect the
win — one host pays for the build, three copy it over the LAN — at the cost of
a longer step. `nixos_selfupdate_build_*` below is there to say whether that
trade is worth making before it is made.

**Superseded pipelines are cancelled, not queued.**
`WOODPECKER_DEFAULT_CANCEL_PREVIOUS_PIPELINE_EVENTS` includes `push`, so a newer
nix-touching commit kills the pipeline for the older one instead of leaving it to
build a commit that is already out of date. Note this does not stop a build
already running on a host: the unit runs under systemd, not under the ssh
session, so it finishes on its own. That is what the backwards guard is for.

**Renovate lands one commit, not three.** Cancellation above is the real fix for
overlapping pipelines, but it is a per-repo setting ticked by hand in the
Woodpecker UI (see one-time setup below), so it does not survive rebuilding the
cluster. The second layer is in `renovate.json`: everything under this
pipeline's `when.path.include` — the root flake, the `lenovo_sa120_fanspeed`
flake and the `zfs-exporter` uv lock — is grouped into a single `nixos hosts`
PR. Ungrouped and automerged, those arrive as separate commits minutes apart,
which is how two pipelines once staged the same four hosts at the same time,
gave every host two concurrent nix builds, and killed each other on the 60m
timeout. Keep that rule's file list in sync with `when.path.include`.

**Staging and rebooting are separate.** A running pipeline pod fires the
`WoodpeckerPipelineRunning` gate, which blocks kured so a drain cannot kill the
pipeline — so a pipeline that waited for a reboot would deadlock against its own
gate. It stages and exits; the rollout Job waits. The Job also has to outlive its
own node being rebooted, which a pipeline pod cannot.

**Reboot order is controlled by creating one sentinel at a time.** kured takes a
cluster-wide lock and picks a node itself, so creating all four sentinels at once
gives an arbitrary order. The Job goes clusterInit server first (borg-2), then
the other servers (borg-0, borg-1), then the agent (borg-3) — the order k3s wants
for a minor-version upgrade. It is fixed rather than derived, so nothing has to
work out whether a given change actually crosses a k3s minor.

**Nothing reboots unless something changed.** The `reboot` verb compares
`/run/booted-system` against `/nix/var/nix/profiles/system` and answers
`UP_TO_DATE` when they match. A flake.lock bump producing an identical toplevel
costs no reboots.

## Credentials

There is no long-lived SSH key and no Woodpecker-native secret.

cert-manager cannot help here — it issues X.509, and upstream sshd only honours
OpenSSH certificates via `TrustedUserCAKeys`. So there is an OpenSSH CA built in
the same spirit as the internal CA:

| | where | lifetime |
|---|---|---|
| CA private key | Secret `deploybot-ssh-ca`, `cert-system` | forever, never leaves the cluster |
| CA public key | `nix/modules/selfupdate/deploybot_user_ca.pub` | forever, committed |
| leaf key + cert | Secret `deploybot-ssh-cert`, `woodpecker` | 12h, reissued every 6h |

Because the hosts' trust anchor is static, rotating the leaf costs them nothing:
no sync DaemonSet, and no window where a host trusts the wrong key. The pipeline
reads the leaf from the Secret at runtime, so it never enters Woodpecker's
database.

## One-time setup

1. Sync `cert-system`. The `deploybot-ssh-ca-bootstrap` ArgoCD sync hook creates
   the CA if it does not already exist.
2. Publish the public half and deploy it:

   ```bash
   ./scripts/setup-ssh-ca.sh
   git add nix/modules/selfupdate/deploybot_user_ca.pub
   lab host deploy borg-2 --boot   # and each other host
   ```

   Until `deploybot_user_ca.pub` exists the module leaves the account, the
   sudoers rules and the sshd config out entirely — the units are still there and
   can be driven by hand.
3. Set the repo's pipeline timeout in the Woodpecker UI (or
   `woodpecker-cli repo update --timeout 60`). `WOODPECKER_DEFAULT_PIPELINE_TIMEOUT`
   only applies to repos activated after it is set.
4. Tick "cancel previous pipelines" for `push` in the repo settings UI. Same
   caveat as the timeout — `WOODPECKER_DEFAULT_CANCEL_PREVIOUS_PIPELINE_EVENTS`
   is only read when a repo is activated. There is no `woodpecker-cli` flag for
   it; the API equivalent is `cancel_previous_pipeline_events` on
   `PATCH /api/repos/{id}`.

## Operating it

```bash
# What is each host doing?
ssh -i <cert> deploybot@10.69.80.12 status

# Force a host to stage the current main, outside CI
ssh borg-2 sudo systemctl start --wait nixos-selfupdate.service
ssh borg-2 sudo journalctl -u nixos-selfupdate.service -n 50

# Roll out pending reboots now, in order
kubectl -n woodpecker create job --from=cronjob/nixos-rollout nixos-rollout-manual
kubectl -n woodpecker logs -f job/nixos-rollout-manual
```

Metrics land in the node-exporter textfile collector. `nixos_selfupdate.prom`
describes where the host *is* and is refreshed every 5 minutes by
`metrics.sh`:

- `nixos_selfupdate_last_success_timestamp_seconds`
- `nixos_selfupdate_reboot_pending`
- `nixos_selfupdate_info{rev,booted_system,staged_system}`

`nixos_selfupdate_build.prom` describes what the last build *did*, and is
written once per run by `build-metrics/nix_build_metrics.py`:

- `nixos_selfupdate_build_duration_seconds`, `..._success`
- `nixos_selfupdate_build_paths_built{machine}` — `machine="local"` is this host
- `nixos_selfupdate_build_paths_substituted{substituter}`
- `nixos_selfupdate_build_substituted_bytes{substituter}` — into the store
- `nixos_selfupdate_build_downloaded_bytes{substituter}` — over the wire
- `nixos_selfupdate_build_seconds{machine}` — time spent building
- `nixos_selfupdate_build_substitute_seconds{substituter}` — time spent fetching
- `nixos_selfupdate_build_timings_available`, `..._parse_errors` — see below

The `substituter` label is what separates cache.nixos.org from a peer, so these
answer whether peer substitution is earning its keep — bytes over seconds gives
the throughput each substituter actually delivered — and
`node_textfile_mtime_seconds{file="nixos_selfupdate_build.prom"}` says how old
the answer is.

The numbers come from `json-log-path`, which upstream marks internal and does
not promise to keep stable (NixOS/nix#13935), so the parser never fails a
build over an event it does not recognise — it counts it in `..._parse_errors`
and carries on. `validate-nix-build-metrics` (see
`nix/modules/selfupdate/build-metrics/`) exercises the parser and its test
suite against a real Nix build in CI, so a format change shows up on the
Renovate PR that bumps the Nix image rather than only on the fleet.

Nix's event stream carries no timestamps, so a follower
(`nix_build_metrics.py timings`) tails the log alongside the build, recording
when each activity's start and stop *arrived*; the report pass joins those
back on by activity id. It polls a plain file every 100ms rather than watching
a named pipe or inotify — a pipe would hang the build if the follower were
late, dead, or backed up, and a measured comparison showed inotify waking the
follower 150,891 times over one 25-second build against 296 for the poll loop,
for the same 3,320 records. A follower that never ran (or died) costs only
timings, which is why `..._timings_available` exists rather than reporting
zero seconds. Durations are per-activity and overlap, so they can exceed
`duration_seconds`.

The log lives in `/run/deploybot/` (tmpfs), sized at roughly 1.6% of the store
bytes substituted — proportional to substitution, not compilation — so it
never comes close to memory pressure. It is truncated at the start of every
run. The metrics themselves are written to `/var/cache/nixos-selfupdate/`,
which is persisted, and published into the (unpersisted) textfile collector
directory; a `systemd-tmpfiles` `C` rule restores the persisted copy after a
rollback so the last build's numbers survive the reboot every rollout ends
with.

Alerting is on the fleet, not on the pipeline, because pipeline status does not
describe where the fleet ended up: a pipeline killed by its own timeout is
reported as `killed` rather than `failure` (so a `when: status: [failure]`
notification step would miss it), and a host's build outlives the pipeline that
asked for it either way. `templates/prometheus-rule-nixos-cd.yaml` covers the
four ways this goes wrong, and Alertmanager routes them to Discord by default:

| alert | fires when |
|---|---|
| `NixosFleetRevisionDrift` | hosts built from different commits for 1h |
| `NixosRebootPendingTooLong` | staged but not booted after 6h |
| `NixosSelfupdateFailed` | `nixos-selfupdate.service` failed on a host |
| `NixosSelfupdateStale` | no successful update in 8 days |

## Fallbacks

- **Host timer.** `nixos-selfupdate.timer` runs daily but the service exits
  immediately unless the last success was over 7.5 days ago — just past the
  weekly cadence of Renovate's flake-update PRs, so it only acts in a week where
  the CI trigger was missed entirely. Its catch-up across reboots depends on
  `/var/lib/systemd/timers` being persisted (`nix/modules/common/impermanence.nix`).
- **Rollout CronJob.** Runs daily and is a no-op when every host is already up to
  date; it exists to roll out anything the host timer staged with no pipeline.
- **Repo source.** Hosts fetch from `git.msng.to` first and fall back to the
  public GitHub mirror. The forge runs on this same cluster, so the mirror is
  what lets a host update itself when the cluster is down. Both are readable
  anonymously, so no repo credentials live on the hosts.

## Files

| what | where |
|---|---|
| host module, scripts | `nix/modules/selfupdate/` |
| build metrics parser | `nix/modules/selfupdate/build-metrics/` |
| peer substituters | `nix/modules/builders/` |
| SSH CA + issuer | `k8s/foundation/cert-system/templates/deploybot-ssh-ca.yaml` |
| RBAC, rollout job, netpol | `k8s/platform/woodpecker/templates/` |
| rollout script, known_hosts | `k8s/platform/woodpecker/files/` |
| kured gate alert | `k8s/foundation/kured/templates/prometheus-rule-woodpecker.yaml` |
| rollout alerts | `k8s/platform/woodpecker/templates/prometheus-rule-nixos-cd.yaml` |
| pipeline | `.woodpecker/deploy-hosts.yaml` |
| update grouping | `renovate.json` (`nixos hosts` rule) |
| setup helpers | `scripts/setup-ssh-ca.sh`, `scripts/update-known-hosts.sh` |
