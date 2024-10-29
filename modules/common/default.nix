{
  config,
  inputs,
  pkgs,
  ...
}: {
  imports = [
    ./sops-nix.nix
    ./store.nix
  ];
}
