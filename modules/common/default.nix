{
  config,
  inputs,
  pkgs,
  ...
}: {
  imports = [
    ./disko.nix
    ./impermanence.nix
    ./sops-nix.nix
    ./store.nix
  ];
}
