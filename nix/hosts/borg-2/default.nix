{...}: {
  imports = [
    ./lenovo-sa120-fanspeed.nix
    ./zfs-storage.nix
    ./nas-backups.nix
    ./nfs-mtls.nix
    ./zfs-textfile-exporter.nix
  ];
  # My TeamGroup NVMe drive has been filling up its write cache and causing
  # writes to timeout multiple times a day.
  #
  # Attempt to alleviate this by reducing the amount of dirty page data that
  # can accumulate in memory before the kernel forces a flush. Default is to
  # use 20% and 10% of ram, which on this system is ~19 GB and 9.4 GB
  # respectively. This should break large, occasional I/O bursts down into
  # smaller, more frequent ones that are less likely to exhaust the SLC cache.
  config.boot.kernel.sysctl = {
    "vm.dirty_background_bytes" = 256 * 1024 * 1024;
    "vm.dirty_bytes" = 1024 * 1024 * 1024;
  };
}
