{...}: {
  imports = [
    ./lenovo-sa120-fanspeed.nix
    ./zfs-storage.nix
    ./longhorn-backups.nix
    ./nfs-mtls.nix
  ];
}
