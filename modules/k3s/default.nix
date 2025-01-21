{
  config,
  inputs,
  pkgs,
  ...
}: {
  imports = [
    ./filesystem.nix
    ./k3s.nix
    ./longhorn.nix
  ];
}
