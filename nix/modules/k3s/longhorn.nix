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
}
