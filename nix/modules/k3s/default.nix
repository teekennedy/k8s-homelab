{...}: {
  imports = [
    ./k3s.nix
    ./kured.nix
    ./longhorn.nix
    ./shutdown-cleanup.nix
    ./gpu-passthrough.nix
    ./intel-vaapi.nix
  ];
}
