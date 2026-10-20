{self, ...}: {
  imports = [
    ./hardware.nix
    ./packages.nix
    ./powersave.nix
    ./smartctl-exporter.nix
  ];
  system.nixos.label = "${self.lastModifiedDate}-${self.shortRev or self.dirtyShortRev or "dirty"}";
}
