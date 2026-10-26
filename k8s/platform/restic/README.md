# restic

Monitoring and upkeep for the restic repositories the NixOS hosts back up to.
The hosts only run `restic backup`; everything else about a repository runs
here.

| Repository    | Written by                                          | Schedule |
| ------------- | --------------------------------------------------- | -------- |
| `hosts`  | every host's `/persistent` (`nix/modules/restic`)   | daily    |
| `nas-backups` | borg-2's NAS backups (`nix/hosts/borg-2/nas-backups.nix`) | weekly   |

## Why the work is split

`hosts` is shared by every host; snapshots are told apart by hostname, and
`forget` keeps its policy per host. In a shared repository only `backup` can
run concurrently: `forget`, `prune` and `check` take an exclusive lock and
would block the other hosts' backups. So the hosts back up with
`--retry-lock 2h`, and one CronJob per repository runs
`unlock` (stale locks only), `forget --prune` and `check` after the backups.

## Metrics and alerts

One [restic-exporter] Deployment per repository lists its snapshots every hour
(`restic_backup_timestamp{repository, client_hostname}`). The timestamp comes
from the repository itself, so it only moves once a snapshot has landed, and a
new host shows up without any config here.

- `ResticBackupStale`: a host's newest snapshot is older than the repository's
  `maxBackupAgeHours`. A host that has never backed up to the repository has
  no series, so it cannot fire for it.
- `ResticRepositoryUnreadable`: the exporter has no data for 2h.
- `ResticMaintenanceStale`: the maintenance CronJob has not succeeded within
  `maxMaintenanceAgeHours`. Failed runs fire `KubeJobFailed`.

kured ignores all three.

The exporter's `TZ` must match the hosts' timezone: it converts snapshot times
with the container's local zone and ignores the recorded offset.

## Secrets

`restic-repositories` is created by `terraform/main.tf`: the
`restic-backup-user` S3 keys, plus each repository's password (keyed by
repository name) read from the NixOS sops files. Re-run `tofu apply` after
rotating either.

## Setting up the `hosts` repository

The hosts do not initialize it themselves (concurrent `restic init` could race),
so create it once before deploying the hosts:

```sh
export RESTIC_REPOSITORY=s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/hosts
export RESTIC_PASSWORD="$(sops decrypt --extract '["restic_hosts_password"]' nix/modules/restic/secrets.enc.yaml)"
eval "$(sops decrypt --extract '["restic_env_file"]' nix/modules/restic/secrets.enc.yaml | sed 's/^/export /')"
restic init
```

[restic-exporter]: https://github.com/ngosang/restic-exporter
