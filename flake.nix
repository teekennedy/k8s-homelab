{
  description = "teekennedy's homelab";
  inputs = {
    colmena.url = "github:zhaofengli/colmena/main";
    nixos-hardware.url = "github:NixOS/nixos-hardware/master";
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
              colmena = inputs.colmena.defaultPackage."${system}";
            })
          ];
        };
        devenv.shells.default = {
          # env.SOPS_AGE_KEY_FILE = ~/.config/sops/age/keys.txt;
          env.KUBECONFIG = "${config.devenv.shells.default.env.DEVENV_STATE}/kube/config";

          # https://devenv.sh/packages/
          packages = with pkgs; [
            age
            colmena
            helmfile
            kubectl
            kubernetes-helm
            kustomize
            opentofu
            sops
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
        packages = {
        };
      };

      flake = {
        colmenaHive = inputs.colmena.lib.makeHive self.outputs.colmena;
        colmena = {
          meta = {
            description = "K3s cluster";
            nixpkgs = import inputs.nixpkgs {
              system = "x86_64-linux";
              overlays = [];
            };
            specialArgs = {
              inherit inputs;
              nixpkgs-master = import inputs.nixpkgs-master {
                system = "x86_64-linux";
                overlays = [];
              };
            };
          };

          defaults = {
            imports = [
              ./modules/common
              ./modules/k3s
              ./modules/users
            ];
          };

          borg-0 = {
            name,
            nodes,
            pkgs,
            ...
          }: {
            imports = [
              ./hosts/common
              ./hosts/borg-0
              inputs.nixos-hardware.nixosModules.common-cpu-intel
              inputs.nixos-facter-modules.nixosModules.facter
              inputs.disko.nixosModules.disko
            ];
            deployment = {
              tags = [];
              # Copy the derivation to the target node and initiate the build there
              buildOnTarget = true;
              targetUser = null; # Defaults to $USER
              targetHost = "borg-0";
            };
            disko.devices.disk.main.device = "/dev/disk/by-id/ata-NT-256_2242_0006245000370";
            disko.longhornDevice = "/dev/disk/by-id/nvme-TEAM_TM8FP4004T_112302210210813";

            facter.reportPath = let
              facterPath = ./hosts/borg-0/facter.json;
            in
              if builtins.pathExists facterPath
              then facterPath
              else throw "Have you forgotten to run nixos-anywhere with `--generate-hardware-config nixos-facter ${facterPath}`?";
            users.users.root.openssh.authorizedKeys.keys = [
              "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
              # GPG SSH key
              "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPQjEqJpz5sOwxeieTNx1UBikeQ43rWnw0oQnjk+Z8z openpgp:0xEC44996F"
            ];
            # TODO add disko.swapFileSize
            services.k3s = {
              role = "server";
              # Leave true for first node in cluster
              clusterInit = true;
            };
            sops.secrets.tkennedy_hashed_password = {
              neededForUsers = true;
            };
          };
        };
        nixosConfigurations = {
          borg-0 =
            inputs.nixpkgs.lib.nixosSystem
            {
              system = "x86_64-linux";
              specialArgs = {
                inherit inputs;
                nixpkgs-master = import inputs.nixpkgs-master {
                  system = "x86_64-linux";
                  overlays = [];
                };
              };
              modules = [
                ./hosts/common
                ./hosts/borg-0
                ./modules/common
                ./modules/k3s
                ./modules/users
                inputs.nixos-hardware.nixosModules.common-cpu-intel
                inputs.nixos-facter-modules.nixosModules.facter
                inputs.disko.nixosModules.disko
                {
                  disko.devices.disk.main.device = "/dev/disk/by-id/ata-NT-256_2242_0006245000370";
                  disko.longhornDevice = "/dev/disk/by-id/nvme-TEAM_TM8FP4004T_112302210210813";

                  facter.reportPath = let
                    facterPath = ./hosts/borg-0/facter.json;
                  in
                    if builtins.pathExists facterPath
                    then facterPath
                    else throw "Have you forgotten to run nixos-anywhere with `--generate-hardware-config nixos-facter ${facterPath}`?";
                  users.users.root.openssh.authorizedKeys.keys = [
                    "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
                    # GPG SSH key
                    "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPQjEqJpz5sOwxeieTNx1UBikeQ43rWnw0oQnjk+Z8z openpgp:0xEC44996F"
                  ];
                  # TODO add disko.swapFileSize
                  services.k3s = {
                    role = "server";
                    # Leave true for first node in cluster
                    clusterInit = true;
                  };
                  sops.secrets.tkennedy_hashed_password = {
                    neededForUsers = true;
                  };
                }
              ];
            };
          # build this with
          # nix build .#nixosConfigurations.bcachefsIso.config.system.build.isoImage
          # the result will be found symlinked to ./result
          # If host is not the same system as iso system, can use --builders flag, e.g.
          # --builders 'ssh://borg-0 x86_64-linux' --store $(readlink -f /tmp)/nix
          # then create a store-fixed symlink based on ./result:
          # ln -s "$(readlink -f /tmp)/nix/$(readlink result)" result-iso
          bcachefsIso = inputs.nixpkgs.lib.nixosSystem {
            system = "x86_64-linux";
            modules = [
              "${inputs.nixos}/nixos/modules/installer/cd-dvd/installation-cd-minimal-new-kernel-no-zfs.nix"
              ./hosts/common/packages.nix
              ({
                lib,
                pkgs,
                ...
              }: {
                # TODO make this and hosts/borg-0/hardware-configuration.nix reference the same data for keys.
                users.users.root.openssh.authorizedKeys.keys = [
                  "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
                  # GPG SSH key
                  "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPQjEqJpz5sOwxeieTNx1UBikeQ43rWnw0oQnjk+Z8z openpgp:0xEC44996F"
                ];

                # Enable the OpenSSH daemon.
                services.openssh = {
                  enable = true;
                  settings = {
                    PasswordAuthentication = lib.mkForce false;
                  };
                };
                boot.supportedFilesystems = ["bcachefs"];
                boot.kernelPackages = lib.mkOverride 0 pkgs.linuxPackages_latest;
                environment.systemPackages = [pkgs.neovim pkgs.nixos-facter (pkgs.writeShellScriptBin "setup-partitions" (builtins.readFile ./scripts/setup-partitions))];
              })
            ];
          };
        };
      };
    };
}
