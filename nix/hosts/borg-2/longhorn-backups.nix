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
    "d ${nasBackupDir}/longhorn 0755 root root -"
    "d ${nasBackupDir}/postgres 0750 s3proxy s3proxy -"
  ];

  # /storage/nas/backups/longhorn is exported separately for the pod CIDR without
  # mTLS: longhorn-manager runs in a pod network namespace, so the kernel cannot
  # reach tlshd (which runs in the host network namespace) for the TLS handshake.
  services.nfs.server.exports = ''
    /storage/nas/backups/longhorn 10.42.0.0/16(rw,async,no_subtree_check,no_root_squash)
  '';

  # Offsite backup of all NAS backup data (longhorn snapshots, postgres base backups) to S3.
  services.restic.backups.nas-backups-weekly = {
    initialize = true;
    passwordFile = config.sops.secrets.restic_repo_password.path;
    environmentFile = config.sops.secrets.restic_env_file.path;
    repository = "s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/nas-backups";
    paths = [nasBackupDir];
    pruneOpts = [
      "--keep-weekly 8"
      "--keep-monthly 12"
    ];
    timerConfig = {
      OnCalendar = "weekly";
      RandomizedDelaySec = "2h";
      Persistent = true;
    };
  };
}
