# Backups

Following the 3-2-1 rule of backups, all of my data will be stored in:
- Its original location (laptop, phone, persistent volume)
- My home NAS (a 6 drive RAIDZ array)
- S3 bucket (the offsite solution)

## What to backup

### NixOS persistent subvolume

Thanks to nix-impermanence, I have a minimal set of files that are necessary for backup, all stored under the /persistent subvolume of the host's root filesystem.

These files are backed up daily to S3 via restic (job: `persistent-daily`).
Restic uses the default cache dir at `/var/cache/restic-backups-<job>` and `/var/cache` is persisted on `/cache`.

**retention policy**: 7 daily, 4 weekly, 12 monthly.

### Kubernetes persistent volumes

I'm using Longhorn as my default storage class for persistent volumes.
Backups land on the NAS via CIFS (borg-2) at `cifs://borg-2.msng.to/longhorn-backups`.
A restic job on borg-2 (`nas-backups-weekly`) backs the NAS backup directory to S3 weekly (repo: `restic/nas-backups`).

Longhorn's role is to get data off the cluster onto the NAS. Restic handles retention. Volumes use one of two groups:

- **default**: Data that should be backed up.
  - backup retention policy: 1 daily. Retention history is managed by restic, not Longhorn.
- **ephemeral**: Tempfiles, caches, and data that can be safely discarded.
  - backup policy: none.
