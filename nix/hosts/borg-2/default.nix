{...}: {
  imports = [
    ./lenovo-sa120-fanspeed.nix
    ./zfs-storage.nix
    ./nas-backups.nix
    ./nfs-mtls.nix
    ./zfs-textfile-exporter.nix
  ];
}
