{
  description = "teekennedy's homelab";
  inputs = {
    deploy-rs.url = "github:serokell/deploy-rs";
    deploy-rs.inputs.nixpkgs.follows = "nixpkgs";
    nixos-facter-modules.url = "github:numtide/nixos-facter-modules";
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixpkgs-master.url = "github:NixOS/nixpkgs/master";
    disko.url = "github:nix-community/disko";
    disko.inputs.nixpkgs.follows = "nixpkgs";
    impermanence.url = "github:nix-community/impermanence";
    devenv.url = "github:cachix/devenv";
    devenv.inputs.nixpkgs.follows = "nixpkgs";
    devenv-root = {
      url = "file+file:///dev/null";
      flake = false;
    };
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
          ];
        };
        devenv.shells.default = {
          devenv.root = let
            devenvRootFileContent = builtins.readFile inputs.devenv-root.outPath;
          in
            pkgs.lib.mkIf (devenvRootFileContent != "") devenvRootFileContent;
          # disable containers to make `nix flake check` pass.
          # https://github.com/cachix/devenv/issues/760
          containers = lib.mkForce {};
          # env.SOPS_AGE_KEY_FILE = ~/.config/sops/age/keys.txt;
          env.KUBECONFIG = "${config.devenv.shells.default.env.DEVENV_STATE}/kube/config";

          # https://devenv.sh/packages/
          packages = with pkgs; [
            age
            deploy-rs
            helmfile
            kubectl
            kubernetes-helm
            kustomize
            nixos-anywhere
            opentofu
            sops
            (writeShellApplication {
              name = "bootstrap-host";
              runtimeInputs = [yq-go sops];
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
        deploy.nodes.borg-0 = {
          hostname = "borg-0";
          profiles.system = {
            user = "root";
            path = inputs.deploy-rs.lib.x86_64-linux.activate.nixos self.nixosConfigurations.borg-0;
          };
        };
        # enable magic rollback and other checks
        checks = builtins.mapAttrs (system: deployLib: deployLib.deployChecks self.deploy) inputs.deploy-rs.lib;
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
                  ./modules/users/defaultUser.nix
                  inputs.nixos-facter-modules.nixosModules.facter
                  {
                    defaultUsername = "tkennedy";
                    networking.hostName = host.hostname;
                    facter.reportPath = let
                      facterPath = ./hosts + "/${host.hostname}" + /facter.json;
                    in
                      if builtins.pathExists facterPath
                      then facterPath
                      else throw "Have you forgotten to run nixos-anywhere with `--generate-hardware-config nixos-facter ${facterPath}`?";
                    sops.defaultSopsFile = let
                      defaultSopsPath = ./hosts + "/${host.hostname}" + /secrets.yaml;
                    in
                      if builtins.pathExists defaultSopsPath
                      then defaultSopsPath
                      else throw "Host ${host.hostname} missing secrets at ${defaultSopsPath}. See README for how to create.";
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
                system.stateVersion = "25.05";
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
                system.stateVersion = "25.05";

                services.k3s = {
                  role = "server";
                };
              }
            ];
          };
          # build this with
          # nix build .#nixosConfigurations.installIso.config.system.build.isoImage
          # the resulting image will be found symlinked to ./result
          # If host is not the same system as iso system, can use --builders flag, e.g.
          # --builders 'ssh://borg-0 x86_64-linux' --store $(readlink -f /tmp)/nix
          # then create a store-fixed symlink based on ./result:
          # ln -s "$(readlink -f /tmp)/nix/$(readlink result)" result-iso
          installIso = inputs.nixpkgs.lib.nixosSystem {
            system = "x86_64-linux";
            modules = [
              "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal-new-kernel-no-zfs.nix"
              ({
                lib,
                pkgs,
                config,
                ...
              }: {
                users.users.root.openssh.authorizedKeys.keyFiles = builtins.map (s: ./modules/users/authorized_keys + "/${s}") (builtins.attrNames (builtins.readDir ./modules/users/authorized_keys));
                networking.hostName = "nixos-installer";
                # Enable the OpenSSH daemon.
                services.openssh = {
                  enable = true;
                  settings = {
                    PasswordAuthentication = lib.mkForce false;
                    PermitRootLogin = "prohibit-password"; # default setting, but good to be explicit
                  };
                };
                boot.kernelPackages = lib.mkOverride 0 pkgs.linuxPackages_latest;
              })
            ];
          };
        };
      };
    };
}
