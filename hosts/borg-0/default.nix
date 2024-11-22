{pkgs, ...}: {
  imports = [
    ./hardware-configuration.nix
  ];
  options = {
    k3s = {
      first-host = true;
    };
  };
}
