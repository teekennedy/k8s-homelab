{
  description = "teekennedy's homelab";
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    systems.url = "github:nix-systems/default";
    devenv.url = "github:cachix/devenv";
    devenv.inputs.nixpkgs.follows = "nixpkgs";
    sops-nix.url = "github:Mic92/sops-nix";
  };

  nixConfig = {
    extra-trusted-public-keys = "devenv.cachix.org-1:w1cLUi8dv3hnoSPGAuibQv+f9TZLr6cv/Hm9XgU50cw=";
    extra-substituters = "https://devenv.cachix.org";
  };

  outputs = {
    self,
    nixpkgs,
    devenv,
    sops-nix,
    systems,
    ...
  } @ inputs: let
    forEachSystem = nixpkgs.lib.genAttrs (import systems);
    inherit (nixpkgs) lib;
  in {
    packages = forEachSystem (system: {
      devenv-up = self.devShells.${system}.default.config.procfileScript;
    });

    devShells =
      forEachSystem
      (system: let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        default = devenv.lib.mkShell {
          inherit inputs pkgs;
          modules = [(import ./devenv.nix {pkgs = pkgs;})];
        };
      });

    colmena = {
      meta = {
        description = "K3s cluster";
        nixpkgs = import nixpkgs {
          system = "x86_64-linux";
          overlays = [];
        };
        specialArgs = { inherit inputs; };
      };

      defaults = {
        imports = [
          ./modules/common
        ];
      };

      borg-0 = {
        name,
        nodes,
        pkgs,
        ...
      }: {
        imports = [
          ./hosts/borg-0
        ];
        deployment = {
          tags = [];
          # Copy the derivation to the target node and initiate the build there
          buildOnTarget = true;
          targetUser = null; # Defaults to $USER
          targetHost = "borg-0.local";
        };

        sops.secrets.tkennedy_hashed_password = {
          neededForUsers = true;
        };
      };
    };
  };
}