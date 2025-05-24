{...}: {
  imports = [
    ./disable-wol.nix
    ./networking.nix
    ./nvme.nix
  ];
}
