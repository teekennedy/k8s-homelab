{...}: {
  imports = [
    ./k3s.nix
    ./longhorn.nix
    ./gpu-passthrough.nix
  ];
}
