# NixOS continuous deployment

Pushes to `main` that touch flake-related files roll out to the borg hosts
automatically, reducing toil.

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
on the next boot.


CI never builds anything and never supplies any code.

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

**Superseded pipelines are cancelled, not queued.**
`WOODPECKER_DEFAULT_CANCEL_PREVIOUS_PIPELINE_EVENTS` includes `push`, so a newer
nix-touching commit kills the pipeline for the older one instead of leaving it to
build a commit that is already out of date. Note this does not stop a build
already running on a host: the unit runs under systemd, not under the ssh
session, so it finishes on its own. That is what the backwards guard is for.

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

Metrics land in the node-exporter textfile collector and are refreshed every
5 minutes:

- `nixos_selfupdate_last_success_timestamp_seconds`
- `nixos_selfupdate_reboot_pending`
- `nixos_selfupdate_info{rev,booted_system,staged_system}`

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
| SSH CA + issuer | `k8s/foundation/cert-system/templates/deploybot-ssh-ca.yaml` |
| RBAC, rollout job, netpol | `k8s/platform/woodpecker/templates/` |
| rollout script, known_hosts | `k8s/platform/woodpecker/files/` |
| kured gate alert | `k8s/foundation/kured/templates/prometheus-rule-woodpecker.yaml` |
| rollout alerts | `k8s/platform/woodpecker/templates/prometheus-rule-nixos-cd.yaml` |
| pipeline | `.woodpecker/deploy-hosts.yaml` |
| setup helpers | `scripts/setup-ssh-ca.sh`, `scripts/update-known-hosts.sh` |
