{...}: {
  imports = [
    ./k3s.nix
    ./kured.nix
    ./longhorn.nix
    ./gpu-passthrough.nix
    ./intel-vaapi.nix
  ];
}
