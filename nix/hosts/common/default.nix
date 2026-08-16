{self, ...}: {
  imports = [
    ./hardware.nix
    ./intel-gpu-exporter.nix
    ./packages.nix
    ./powersave.nix
  ];
  system.nixos.label = "${self.lastModifiedDate}-${self.shortRev or self.dirtyShortRev or "dirty"}";
}
