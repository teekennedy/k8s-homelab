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
    lenovo-sa120-fanspeed = pkgs.callPackage ./lenovo_sa120_fanspeed.nix {};
  in {
    packages.${system}.lenovo-sa120-fanspeed = lenovo-sa120-fanspeed;
    devShells.${system}.default = pkgs.mkShell {
      name = "lenovo-sa120-fanspeed-shell";
      buildInputs = [
        pkgs.sg3_utils
        lenovo-sa120-fanspeed
      ];
    };
  };
}
