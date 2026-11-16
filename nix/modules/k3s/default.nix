{...}: {
  imports = [
    ./k3s.nix
    ./api-vip.nix
    ./kured.nix
    ./longhorn.nix
    ./shutdown-cleanup.nix
    ./gpu-passthrough.nix
    ./intel-vaapi.nix
  ];
}
