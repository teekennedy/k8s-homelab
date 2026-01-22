{config, ...}: let
  longhornBackupDir = "/storage/nas/backups/longhorn";
in {
  # ensure nas directories exist
  # Note this does not create tmpfs mounts - these directories will be created on top of zfs
  # See man tmpfiles.d for more info
  systemd.tmpfiles.rules = [
    "d /storage/nas/backups 0755 root root -"
    "d ${longhornBackupDir} 0755 root root -"
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
  # "insecure" export option means that the client doesn't have to connect from a privileged port.
  services.nfs.server.exports = ''
    /storage/nas 10.69.80.0/25(rw,async,no_subtree_check,no_root_squash,fsid=0,insecure) 10.42.0.0/16(rw,async,no_subtree_check,no_root_squash,fsid=0,insecure)
  '';

  networking.firewall.allowedTCPPorts = [2049];

  services.restic.backups.longhorn-weekly = {
    initialize = true;
    passwordFile = config.sops.secrets.restic_repo_password.path;
    environmentFile = config.sops.secrets.restic_env_file.path;
    repository = "s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/longhorn";
    paths = [longhornBackupDir];
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
