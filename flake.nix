{
  description = "teekennedy's homelab";
  inputs = {
    colmena.url = "github:zhaofengli/colmena/main";
    deploy-rs.url = "github:serokell/deploy-rs";
    nixos-facter-modules.url = "github:numtide/nixos-facter-modules";
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixpkgs-master.url = "github:NixOS/nixpkgs/master";
    disko.url = "github:nix-community/disko";
    disko.inputs.nixpkgs.follows = "nixpkgs";
    systems.url = "github:nix-systems/default";
    devenv.url = "github:cachix/devenv";
    devenv.inputs.nixpkgs.follows = "nixpkgs";
    sops-nix.url = "github:Mic92/sops-nix";
    flake-parts.url = "github:hercules-ci/flake-parts";
  };

  nixConfig = {
    extra-trusted-public-keys = "devenv.cachix.org-1:w1cLUi8dv3hnoSPGAuibQv+f9TZLr6cv/Hm9XgU50cw=";
    extra-substituters = "https://devenv.cachix.org";
  };

  outputs = inputs @ {
    flake-parts,
    self,
    ...
  }:
    flake-parts.lib.mkFlake {
      inherit inputs;
    } {
      imports = [
        inputs.devenv.flakeModule
      ];
      systems = ["aarch64-darwin" "x86_64-linux"];
      perSystem = {
        config,
        self',
        inputs',
        pkgs,
        lib,
        system,
        ...
      }: {
        _module.args.pkgs = import inputs.nixpkgs {
          inherit system;

          overlays = [
            (final: prev: rec {
              kubernetes-helm = prev.wrapHelm prev.kubernetes-helm {
                plugins = with prev.kubernetes-helmPlugins; [
                  helm-secrets
                  helm-diff
                  helm-s3
                  helm-git
                ];
              };

              helmfile = prev.helmfile-wrapped.override {
                inherit (kubernetes-helm) pluginsDir;
              };
              # Use k3s release with graceful shutdown patches from nixpkgs master branch
              # https://github.com/NixOS/nixpkgs/issues/255783
              k3s = inputs.nixpkgs-master.pkgs.k3s;
            })
            # Overlay deploy-rs package from nixpkgs to take advantage of binary cache
            inputs.deploy-rs.overlay # or deploy-rs.overlays.default
            (final: prev: {
              deploy-rs = {
                inherit (pkgs) deploy-rs;
                lib = prev.deploy-rs.lib;
              };
            })
          ];
        };
        devenv.shells.default = {
          # env.SOPS_AGE_KEY_FILE = ~/.config/sops/age/keys.txt;
          env.KUBECONFIG = "${config.devenv.shells.default.env.DEVENV_STATE}/kube/config";

          # https://devenv.sh/packages/
          packages = with pkgs; [
            age
            helmfile
            kubectl
            kubernetes-helm
            kustomize
            nixos-anywhere
            opentofu
            sops
            (writeShellApplication {
              name = "bootstrap-host";
              runtimeInputs = [yq sops];
              text = builtins.readFile ./scripts/bootstrap-host.sh;
            })
          ];

          enterShell = ''
          '';

          # https://devenv.sh/languages/
          languages.nix.enable = true;

          # https://devenv.sh/scripts/
          # scripts.hello.exec = "echo hello from $GREET";

          # https://devenv.sh/pre-commit-hooks/
          pre-commit.hooks = {
            # Nix code formatter
            alejandra.enable = true;
            # Terraform code formatter
            terraform-format.enable = true;
            # YAML linter
            yamllint.enable = true;
          };

          # https://devenv.sh/processes/
          # processes.ping.exec = "ping example.com";
        };
      };

      flake = {
        deploy.nodes.borg-0.profiles.system = flake-parts.withSystem "x86_64-linux" (
          ctx @ {pkgs}: {
            user = "tkennedy";
            path = pkgs.deploy-rs.lib.activate.nixos self.nixosConfigurations.borg-0;
            # enable magic rollback
            checks = builtins.mapAttrs (system: deployLib: deployLib.deployChecks self.deploy) pkgs.deploy-rs.lib;
          }
        );
        nixosConfigurations = let
          borgSystem = host:
            inputs.nixpkgs.lib.nixosSystem {
              system = host.system;
              specialArgs = {
                inherit inputs;
                nixpkgs-master = import inputs.nixpkgs-master {
                  system = host.system;
                  overlays = [];
                };
              };
              modules =
                [
                  ./hosts/common
                  (./hosts + "/${host.hostname}")
                  ./modules/common
                  ./modules/users/tkennedy.nix
                  inputs.nixos-facter-modules.nixosModules.facter
                  inputs.disko.nixosModules.disko
                  {
                    facter.reportPath = let
                      facterPath = ./hosts + "/${host.hostname}" + /facter.json;
                    in
                      if builtins.pathExists facterPath
                      then facterPath
                      else throw "Have you forgotten to run nixos-anywhere with `--generate-hardware-config nixos-facter ${facterPath}`?";
                  }
                ]
                ++ host.modules;
            };
        in {
          borg-0 = borgSystem {
            hostname = "borg-0";
            system = "x86_64-linux";
            modules = [
              ./modules/k3s
              ({config, ...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/ata-NT-256_2242_0006245000370";
                disko.longhornDevice = "/dev/disk/by-id/nvme-TEAM_TM8FP4004T_112302210210813";
                services.k3s = {
                  role = "server";
                  # Leave true for first node in cluster
                  clusterInit = true;
                };
              })
            ];
          };
          borg-1 = borgSystem {
            hostname = "borg-1";
            system = "x86_64-linux";
            modules = [
              # ./modules/k3s
              {
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-Aura_Pro_X2_OW23012314C43991F";
                disko.longhornDevice = null;

                services.k3s = {
                  role = "server";
                };
              }
            ];
          };
          # build this with
          # nix build .#nixosConfigurations.installIso.config.system.build.isoImage
          # the result will be found symlinked to ./result
          # If host is not the same system as iso system, can use --builders flag, e.g.
          # --builders 'ssh://borg-0 x86_64-linux' --store $(readlink -f /tmp)/nix
          # then create a store-fixed symlink based on ./result:
          # ln -s "$(readlink -f /tmp)/nix/$(readlink result)" result-iso
          installIso = inputs.nixpkgs.lib.nixosSystem {
            system = "x86_64-linux";
            modules = [
              "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal-new-kernel-no-zfs.nix"
              ./hosts/common/packages.nix
              ({
                lib,
                pkgs,
                ...
              }: {
                # TODO make this and hosts/borg-0/hardware-configuration.nix reference the same data for keys.
                users.users.nixos.openssh.authorizedKeys.keys = [
                  "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
                  # GPG SSH key
                  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPQjEqJpz5sOwxeieTNx1UBikeQ43rWnw0oQnjk+Z8z openpgp:0xEC44996F"
                ];
                security.sudo.wheelNeedsPassword = false;

                # Enable the OpenSSH daemon.
                services.openssh = {
                  enable = true;
                  settings = {
                    PasswordAuthentication = lib.mkForce false;
                  };
                };
                boot.supportedFilesystems = ["bcachefs"];
                boot.kernelPackages = lib.mkOverride 0 pkgs.linuxPackages_latest;
              })
            ];
          };
        };
      };
    };
}
