{
  description = "A package to set the fan speed on Lenovo SA120 DAS";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
  };

  outputs = {nixpkgs, ...}: let
    system = "x86_64-linux";
    pkgs = nixpkgs.legacyPackages.${system};

    lenovo-sa120-fanspeed = pkgs.callPackage ./lenovo_sa120_fanspeed.nix {
      python3 = pkgs.python3;
      makeWrapper = pkgs.makeWrapper;
      sg3_utils = pkgs.sg3_utils;
      pkgs = pkgs;
    };
  in {
    packages.${system}.default = lenovo-sa120-fanspeed;

    devShells.${system}.default = pkgs.mkShell {
      packages = [
        pkgs.sg3_utils
        lenovo-sa120-fanspeed
      ];
    };
  };
}
