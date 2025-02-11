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
    sops-nix.inputs.nixpkgs.follows = "nixpkgs";
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
            argocd
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
              runtimeInputs = [yq-go sops ssh-to-age mkpasswd];
              text = builtins.readFile ./scripts/bootstrap-host.sh;
            })
            (pkgs.stdenv.mkDerivation {
              name = "hacks";
              propagatedBuildInputs = [
                (python312.withPackages (pythonPackages:
                  with pythonPackages; [
                    jinja2
                    kubernetes
                    mkdocs-material
                    netaddr
                    pexpect
                    rich
                    kanidm
                  ]))
              ];
              dontUnpack = true;
              installPhase = "install -Dm755 ${./scripts/hacks} $out/bin/hacks";
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

      flake = let
        borgHosts = [
          {
            hostname = "borg-0";
            system = "x86_64-linux";
            modules = [
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
          }
          {
            hostname = "borg-1";
            system = "x86_64-linux";
            modules = [
              ({lib, ...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-Aura_Pro_X2_OW23012314C43991F";
                disko.longhornDevice = "/dev/disk/by-id/usb-ADATA_SX_8200PNP_012345678906-0:0";
                system.stateVersion = "25.05";

                systemd.network.networks."10-lan" = {
                  matchConfig.Name = "ens9";
                  networkConfig = {
                    Address = "10.69.80.11/25";
                    Gateway = ["10.69.80.1"];
                    DNS = ["10.69.80.1"];
                  };
                };
                # Don't sleep when laptop is closed
                services.logind.lidSwitch = "ignore";

                services.k3s = {
                  enable = true;
                  role = "server";
                  serverAddr = "https://10.69.80.10:6443";
                };
              })
            ];
          }
          {
            hostname = "borg-2";
            system = "x86_64-linux";
            modules = [
              ({lib, ...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-WD_BLACK_SN770_1TB_23011J801757";
                disko.longhornDevice = "/dev/disk/by-id/nvme-TEAM_TM8FFD004T_TPBF2404020050100710";
                system.stateVersion = "25.05";
                hardware.cpu.intel.updateMicrocode = true;

                systemd.network.networks."10-lan" = {
                  matchConfig.Name = "enp3s0f0np0";
                  networkConfig = {
                    Address = "10.69.80.12/25";
                    Gateway = ["10.69.80.1"];
                    DNS = ["10.69.80.1"];
                  };
                };

                services.k3s = {
                  enable = true;
                  role = "server";
                  serverAddr = "https://10.69.80.10:6443";
                };
              })
            ];
          }
        ];
      in {
        # enable magic rollback and other checks
        checks = builtins.mapAttrs (system: deployLib: deployLib.deployChecks self.deploy) inputs.deploy-rs.lib;
        deploy.nodes = builtins.listToAttrs (builtins.map (host: {
            name = host.hostname;
            value = {
              hostname = host.hostname;
              profiles.system = {
                user = "root";
                path = inputs.deploy-rs.lib.${host.system}.activate.nixos self.nixosConfigurations.${host.hostname};
              };
            };
          })
          borgHosts);
        nixosConfigurations =
          (builtins.listToAttrs (builtins.map (host: {
              name = host.hostname;
              value = inputs.nixpkgs.lib.nixosSystem {
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
                    ./modules/k3s
                    ./modules/users/defaultUser.nix
                    inputs.nixos-facter-modules.nixosModules.facter
                    {
                      defaultUsername = "tkennedy";
                      networking.hostName = host.hostname;
                      # Pin nixpkgs to flake input
                      nix.registry.nixpkgs.flake = inputs.nixpkgs;
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
            })
            borgHosts))
          // {
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
                # The zfs kernel module 2.3.0 is incompatible with kernel 6.13 or higher
                # https://github.com/NixOS/nixpkgs/blob/799ba5bffed04ced7067a91798353d360788b30d/pkgs/os-specific/linux/zfs/2_3.nix
                # Falling back to "old" kernel 6.12.12 for now
                "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix"
                # "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal-new-kernel.nix"
                ({
                  lib,
                  pkgs,
                  config,
                  ...
                }: {
                  users.users.root.openssh.authorizedKeys.keyFiles = builtins.map (s: ./modules/users/authorized_keys + "/${s}") (builtins.attrNames (builtins.readDir ./modules/users/authorized_keys));
                  networking.hostName = "nixos-installer";
                  # Pin nixpkgs to flake input
                  nix.registry.nixpkgs.flake = inputs.nixpkgs;
                  environment.systemPackages = [
                    pkgs.nixos-facter
                  ];
                  # Enable the OpenSSH daemon.
                  services.openssh = {
                    enable = true;
                    settings = {
                      PasswordAuthentication = lib.mkForce false;
                      PermitRootLogin = "prohibit-password"; # default setting, but good to be explicit
                    };
                  };
                })
              ];
            };
          };
      };
    };
}
