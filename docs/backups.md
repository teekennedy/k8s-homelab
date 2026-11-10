# Backups

Following the 3-2-1 rule of backups, all of my data will be stored in:
- Its original location (laptop, phone, persistent volume)
- My home NAS (a 6 drive RAIDZ array)
- S3 bucket (the offsite solution)

## Retention

Restic keeps the history for everything that goes offsite. Like Backblaze's
30-day file history, every daily snapshot is kept for 30 days. Older
month-end snapshots act as backstops for anything noticed after that:

- **daily**: every snapshot from the last 30 days.
- **monthly**: the last snapshot of each of the last 4 calendar months, so
  backstops roughly 30-60 and 60-90 days old.

Forget, prune and check run in-cluster (`k8s/platform/restic`), not on the
hosts. The exact policy and how the S3 lifecycle rules interact with it are in
that chart's README.

## What to backup

### NixOS persistent subvolume

Thanks to nix-impermanence, I have a minimal set of files that are necessary for backup, all stored under the /persistent subvolume of the host's root filesystem.

These files are backed up daily (02:00-04:00) to S3 via restic (job: `persistent-daily`, repo: `restic/hosts`).
Restic uses the default cache dir at `/var/cache/restic-backups-<job>` and `/var/cache` is persisted on `/cache`.

### Kubernetes persistent volumes

I'm using Longhorn as my default storage class for persistent volumes.
Backups land on the NAS via CIFS (borg-2) at `cifs://borg-2.msng.to/longhorn-backups`.
Apps that back themselves up (e.g. VictoriaMetrics, the *arr apps) write to NFS volumes under the same NAS backup directory.
A restic job on borg-2 (`nas-backups-daily`) backs the NAS backup directory to S3 daily at 02:00-03:00, after the cluster's 01:00-02:00 backups (repo: `restic/nas-backups`).

Longhorn's role is to get data off the cluster onto the NAS. Restic handles retention. Volumes use one of two groups:

- **default**: Data that should be backed up.
  - backup retention policy: 1 daily. Retention history is managed by restic, not Longhorn.
- **ephemeral**: Tempfiles, caches, and data that can be safely discarded.
  - backup policy: none.
