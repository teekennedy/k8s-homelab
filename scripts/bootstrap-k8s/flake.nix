{
  description = "Bootstraps k8s-homelab hosts after initial ArgoCD install.";

  inputs = {
    flake-parts.url = "github:hercules-ci/flake-parts";
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = inputs @ {flake-parts, ...}:
    flake-parts.lib.mkFlake {inherit inputs;} {
      imports = [];
      systems = ["x86_64-linux" "aarch64-linux"];
      perSystem = {pkgs, ...}: let
        bootstrap-k8s-script = pkgs.stdenv.mkDerivation {
          name = "bootstrap-k8s";
          propagatedBuildInputs = [
            (pkgs.python3.withPackages (pythonPackages:
              with pythonPackages; [
                jinja2
                kubernetes
                mkdocs-material
                netaddr
                pexpect
                rich
                # kanidm
              ]))
          ];
          dontUnpack = true;
          installPhase = "install -Dm755 ${./bootstrap-k8s.py} $out/bin/bootstrap-k8s";
        };
      in {
        devShells.default = pkgs.mkShell {
          packages = [
            bootstrap-k8s-script
            pkgs.kanidm
          ];
        };
        packages.default = bootstrap-k8s-script;
      };
      flake = {
        # The usual flake attributes can be defined here, including system-
        # agnostic ones like nixosModule and system-enumerating ones, although
        # those are more easily expressed in perSystem.
      };
    };
}
