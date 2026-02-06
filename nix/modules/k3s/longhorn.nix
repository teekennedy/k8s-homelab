{
  config,
  pkgs,
  ...
}: {
  environment.systemPackages = [pkgs.nfs-utils];
  services.openiscsi = {
    enable = true;
    name = "${config.networking.hostName}-initiatorhost";
  };
  # Place iscsid under /bin where longhorn expects it
  # https://github.com/longhorn/longhorn/issues/2166
  systemd.services.iscsid.serviceConfig = {
    PrivateMounts = "yes";
    BindPaths = "/run/current-system/sw/bin:/bin";
  };
  # Symlink mount binary to /usr/bin/mount - necessary for longhorn RWX volumes which are mounted using nfs
  systemd.tmpfiles.rules = [
    "L /usr/bin/mount - - - - /run/current-system/sw/bin/mount"
  ];
}
