{pkgs, ...}: {
  imports = [
    ./hardware-configuration.nix
    ./disable-wol.nix
  ];
}
