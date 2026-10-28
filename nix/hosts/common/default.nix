{...}: {
  imports = [
    ./hardware.nix
    ./packages.nix
    ./powersave.nix
    ./smartctl-exporter.nix
  ];
  # Keep the commit out of the system (no rev in system.nixos.label or
  # system.configurationRevision): a host's system must change only when its
  # configuration does, so that a deploy can skip and not reboot the others.
}
