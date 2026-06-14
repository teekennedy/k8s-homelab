{...}: {
  imports = [
    ./disable-wol.nix
    ./networking.nix
    ./nvme.nix
    ./nfs-mtls.nix
  ];
}
