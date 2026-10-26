{
  config,
  lib,
  pkgs,
  ...
}: let
  backupName = "persistent-daily";
  # Shared by every host; snapshots are told apart by their hostname. Retention
  # (forget/prune) and check run in-cluster, see k8s/platform/restic.
  repository = "s3:s3.us-west-2.amazonaws.com/missingtoken-backup-us-west-2/restic/hosts";
  hasPersistent =
    (config ? fileSystems)
    && (config.fileSystems ? "/persistent");
in {
  config = lib.mkIf hasPersistent {
    # Make the restic cli available for interactive use
    environment.systemPackages = [pkgs.restic];

    sops.secrets.restic_env_file = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) {
      sopsFile = ./secrets.enc.yaml;
      owner = config.users.users.root.name;
      group = config.users.users.root.group;
      mode = "0400";
    };
    sops.secrets.restic_persistent_password = {
      sopsFile = ./secrets.enc.yaml;
      owner = config.users.users.root.name;
      group = config.users.users.root.group;
      mode = "0400";
    };

    services.restic.backups.${backupName} = {
      # The repository is initialized once by hand; concurrent `restic init`
      # from several hosts could race on its config.
      initialize = false;
      passwordFile = config.sops.secrets.restic_persistent_password.path;
      environmentFile = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) config.sops.secrets.restic_env_file.path;
      inherit repository;
      paths = ["/persistent"];
      # No pruneOpts or checkOpts: both take an exclusive lock on the shared
      # repository and would block the other hosts' backups.
      extraBackupArgs = [
        # Wait out the in-cluster prune/check rather than failing.
        "--retry-lock 2h"
        # exclude a folder’s content if it contains the special CACHEDIR.TAG file, but keep CACHEDIR.TAG
        # https://bford.info/cachedir/
        "--exclude-caches"
        # prevent restic from crossing filesystem boundaries and subvolumes when performing a backup
        "--one-file-system"
      ];
      timerConfig = {
        OnCalendar = "daily";
        RandomizedDelaySec = "2h";
        Persistent = true;
      };
    };
  };
}
