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

  # NFSv4-only: disable v2/v3 while still allowing the default nfs-server unit
  # dependencies (rpcbind/mountd) to start so nfs-server can come up cleanly.
  services.nfs.server.enable = true;
  services.nfs.settings.nfsd = {
    vers2 = "n";
    vers3 = "n";
    vers4 = "y";
    "vers4.1" = "y";
    "vers4.2" = "y";
  };
  # NFSv4 pseudoroot restricted to node LAN only; mTLS required.
  # Pod CIDR removed — pods access /storage/nas/backups via the s3proxy service instead.
  # root_squash (default) is intentional: nodes only traverse this pseudoroot,
  # they never write files to /storage/nas itself. Subdir exports (/k8s) retain
  # no_root_squash where the CSI driver needs root identity for PV provisioning.
  services.nfs.server.exports = ''
    /storage/nas 10.69.80.0/25(rw,async,no_subtree_check,fsid=0,insecure,xprtsec=mtls)
    /storage/nas/backups/longhorn 10.42.0.0/16(rw,async,no_subtree_check,no_root_squash,insecure)
  '';

  networking.firewall.allowedTCPPorts = [2049];

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
