{self, ...}: {
  imports = [
    ./hardware.nix
    ./packages.nix
    ./powersave.nix
  ];
  system.nixos.label = "${self.lastModifiedDate}-${self.shortRev or self.dirtyShortRev or "dirty"}";
}
