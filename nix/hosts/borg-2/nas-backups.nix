{config, ...}: let
  nasBackupDir = "/storage/nas/backups";
in {
  # s3proxy runs as this UID inside the container; NFS maps UIDs by number,
  # so the backup subdirs must be owned by the same UID on the server.
  users.groups.s3proxy = {gid = 2001;};
  users.users.s3proxy = {
    uid = 2001;
    group = "s3proxy";
    isSystemUser = true;
  };

  # ensure nas backup directories exist on the ZFS dataset.
  # Note this does not create tmpfs mounts - these directories will be created on top of zfs
  # See man tmpfiles.d for more info
  systemd.tmpfiles.rules = [
    "d ${nasBackupDir} 0755 root root -"
    "d ${nasBackupDir}/postgres 0750 s3proxy s3proxy -"
  ];

  sops.secrets.restic_repo_password = {
    owner = config.users.users.root.name;
    group = config.users.users.root.group;
    mode = "0400";
  };

  # Offsite backup of all NAS backup data (longhorn snapshots, postgres base backups,
  # victoriametrics) to S3. Retention (forget/prune) and check run in-cluster,
  # see k8s/platform/restic.
  services.restic.backups.nas-backups-daily = {
    initialize = true;
    passwordFile = config.sops.secrets.restic_repo_password.path;
    environmentFile = config.sops.secrets.restic_env_file.path;
    repository = "s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/nas-backups";
    paths = [nasBackupDir];
    # Wait out the in-cluster prune/check rather than failing.
    extraBackupArgs = ["--retry-lock 2h"];
    # After the cluster's 01:00-02:00 backups land on the NAS. A run takes
    # ~40m, so starting by 03:00 keeps it clear of kured's 04:00 reboot window.
    timerConfig = {
      OnCalendar = "*-*-* 02:00:00";
      RandomizedDelaySec = "1h";
      Persistent = true;
    };
  };
}
