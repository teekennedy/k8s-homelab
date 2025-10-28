# Backups

Following the 3-2-1 rule of backups, all of my data will be stored in:
- Its original location (laptop, phone, persistent volume)
- My home NAS (a 6 drive RAIDZ array)
- S3 bucket (the offsite solution)

## Retention Tiers

## What to backup

### NixOS persistent subvolume

Thanks to nix-impermanence, I have a minimal set of files that are necessary for backup, all stored under the /persistent subvolume of the host's root filesystem.

These files are backed up nightly to S3 thanks to a restic cronjob. TODO also backup to NAS.

**retention policy**: 7 daily, 4 weekly, 12 monthly.

### Kubernetes persistent volumes

I'm using Longhorn as my default storage class for persistent volumes.
Volumes are organized into different retention tiers based on how much it would suck to lose its data:

- **critical**: Data that if lost, I would never be able to get back: photos, videos, and personal projects.
  - snapshot policy: 24 hourly.
  - retention policy: 7 daily, 4 weekly, 12 monthly.
- **default**: Stuff that would suck to have to setup again, but not the end of the world. Manually configured services, downloaded media, game saves.
  - snapshot policy: 1 daily.
  - retention policy: 3 daily, 2 weekly, 6 monthly. (roughly 1/2 of critical)
- **secondary**: Some services have their own backup solution, such as a database dump or a config file export.
  This is used as the services primary backup solution, bumping Longhorn backups to secondary solution.
  These backups are only needed in cases where the primary strategy fails.
  - snapshot policy: 1 daily.
  - retention policy: 1 daily, 1 weekly, 1 monthly, 1 yearly.
- **ephemeral**: Tempfiles and cached data that should be excluded from backup.
  - snapshot policy: none.
  - rentention policy: none.

