{...}: {
  imports = [
    ./disko.nix
    ./impermanence.nix
    ./journald.nix
    ./netconsole.nix
    ./sops-nix.nix
    ./nix.nix
    ./io-tuning.nix
    ./textfile-collector.nix
  ];
}
