{
  config,
  lib,
  ...
}: let
  backupName = "persistent";
  repoBase = "s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/hosts";
  hasPersistent =
    (config ? fileSystems)
    && (config.fileSystems ? "/persistent");
in {
  config = lib.mkIf hasPersistent {
    sops.secrets.restic_env_file = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) {
      sopsFile = ./secrets.enc.yaml;
      owner = config.users.users.root.name;
      group = config.users.users.root.group;
      mode = "0400";
    };
    sops.secrets.restic_repo_password = {
      owner = config.users.users.root.name;
      group = config.users.users.root.group;
      mode = "0400";
    };

    # TODO integrate with prometheus, either through
    # services.restic.server.prometheus = true;
    # or
    # services.prometheus.exporters.restic.enable = true;
    services.restic.backups.${backupName} = {
      initialize = true;
      passwordFile = config.sops.secrets.restic_repo_password.path;
      environmentFile = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) config.sops.secrets.restic_env_file.path;
      repository = "${repoBase}/${config.networking.hostName}";
      paths = ["/persistent"];
      extraBackupArgs = [
        # exclude a folder’s content if it contains the special CACHEDIR.TAG file, but keep CACHEDIR.TAG
        # https://bford.info/cachedir/
        "--exclude-caches"
        # prevent restic from crossing filesystem boundaries and subvolumes when performing a backup
        "--one-file-system"
      ];
      pruneOpts = [
        "--keep-daily 7"
        "--keep-weekly 4"
        "--keep-monthly 12"
      ];
      timerConfig = {
        OnCalendar = "daily";
        RandomizedDelaySec = "1h";
        Persistent = true;
      };
    };
  };
}
