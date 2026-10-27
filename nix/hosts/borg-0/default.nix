{...}: {
  imports = [
    ./network.nix
    ./nfs-mtls.nix
  ];

  # Memory-controller investigation, see nix/modules/memory-testing/README.md.
  services.memory-testing = {
    enable = true;
    biosSetup.enable = true;
  };
}
