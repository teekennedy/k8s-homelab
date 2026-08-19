{
  description = "lab - unified CLI for k8s-homelab management";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
  }:
    (flake-utils.lib.eachDefaultSystem (system: let
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      packages = {
        default = self.packages.${system}.lab;
        lab = pkgs.callPackage ./default.nix {};
      };

      apps.default = {
        type = "app";
        program = "${self.packages.${system}.lab}/bin/lab";
      };
    }))
    // {
      devenvModules.default = import ./devenv.nix;
    };
}
