{
  config,
  inputs,
  pkgs,
  ...
}: {
  imports = [
    ./k3s.nix
    ./longhorn.nix
  ];
}
