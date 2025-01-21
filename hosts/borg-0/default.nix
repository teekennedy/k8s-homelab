{pkgs, ...}: {
  imports = [
    ./disable-wol.nix
    ./networking.nix
  ];
}
