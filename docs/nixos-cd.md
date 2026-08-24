# NixOS continuous deployment

Pushes to `main` that touch flake-related files roll out to the borg hosts
automatically, reducing toil.

## Overview

```
push to main (flake.nix, flake.lock, nix/**)
  │
  ├─ .woodpecker/deploy-hosts.yaml
  │    step "stage":   ssh deploybot@host "deploy <sha>"    (sequential, fail-fast)
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
   `woodpecker-cli repo update --timeout 30`). `WOODPECKER_DEFAULT_PIPELINE_TIMEOUT`
   only applies to repos activated after it is set.

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
| pipeline | `.woodpecker/deploy-hosts.yaml` |
| setup helpers | `scripts/setup-ssh-ca.sh`, `scripts/update-known-hosts.sh` |
