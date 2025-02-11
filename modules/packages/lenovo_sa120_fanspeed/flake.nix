{
  description = "A package to set the fan speed on Lenovo SA120 DAS";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = {
    self,
    nixpkgs,
  }: let
    system = "x86_64-linux";
    pkgs = nixpkgs.legacyPackages.${system};
  in {
    packages.x86_64-linux.default = pkgs.callPackage ./lenovo_sa120_fanspeed.nix {};
  };
}
