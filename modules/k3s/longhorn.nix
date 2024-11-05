{
  config,
  inputs,
  lib,
  pkgs,
  ...
}: {
  environment.systemPackages = [pkgs.nfs-utils];
  services.openiscsi = {
    enable = true;
    name = "${config.networking.hostName}-initiatorhost";
  };
}
