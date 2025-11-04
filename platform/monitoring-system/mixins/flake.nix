{
  description = "A flake for building and customizing prometheus mixins";

  # Flake inputs
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  # Flake outputs
  outputs = {...} @ inputs: let
    supportedSystems = [
      "x86_64-linux" # 64-bit Intel/AMD Linux
      "aarch64-linux" # 64-bit ARM Linux
      "x86_64-darwin" # 64-bit Intel macOS
      "aarch64-darwin" # 64-bit ARM macOS
    ];

    forEachSupportedSystem = f:
      inputs.nixpkgs.lib.genAttrs supportedSystems (
        system:
          f {
            inherit system;
            pkgs = import inputs.nixpkgs {
              inherit system;
              config.allowUnfree = true;
            };
          }
      );
  in {
    # Development environments output by this flake

    # To activate the default environment:
    # nix develop
    # Or if you use direnv:
    # direnv allow
    devShells = forEachSupportedSystem (
      {pkgs, ...}: {
        # Run `nix develop` to activate this environment or `direnv allow` if you have direnv installed
        default = pkgs.mkShellNoCC {
          # The Nix packages provided in the environment
          packages = with pkgs; [
            jsonnet-bundler
            go-jsonnet
            gojsontoyaml
          ];

          # Set any environment variables for your development environment
          env = {};

          # Add any shell logic you want executed when the environment is activated
          shellHook = "";
        };
      }
    );
  };
}
