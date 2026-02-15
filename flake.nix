{
  description = "teekennedy's homelab";
  inputs = {
    deploy-rs.url = "github:serokell/deploy-rs?ref=master";
    deploy-rs.inputs.nixpkgs.follows = "nixpkgs";
    nixos-facter-modules.url = "github:nix-community/nixos-facter-modules?ref=main";
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    disko.url = "github:nix-community/disko?ref=master";
    disko.inputs.nixpkgs.follows = "nixpkgs";
    impermanence.url = "github:nix-community/impermanence?ref=master";
    lab.url = "./cmd/lab";
    lab.inputs.nixpkgs.follows = "nixpkgs";
    lenovo_sa120_fanspeed.url = "./nix/modules/packages/lenovo_sa120_fanspeed";
    lenovo_sa120_fanspeed.inputs.nixpkgs.follows = "nixpkgs";
    sops-nix.url = "github:Mic92/sops-nix?ref=master";
    sops-nix.inputs.nixpkgs.follows = "nixpkgs";
    flake-parts.url = "github:hercules-ci/flake-parts?ref=main";
  };

  outputs = inputs @ {
    flake-parts,
    self,
    ...
  }:
    flake-parts.lib.mkFlake {
      inherit inputs;
    } {
      systems = ["aarch64-darwin" "x86_64-linux"];
      perSystem = {system, ...}: {
        _module.args.pkgs = import inputs.nixpkgs {
          inherit system;

          overlays = [
            (_: prev: {deploy-rs = inputs.deploy-rs.outputs.packages.${prev.stdenv.hostPlatform.system}.deploy-rs;})
          ];
        };
      };

      flake = let
        borgHosts = [
          {
            hostname = "borg-0";
            system = "x86_64-linux";
            modules = [
              ({...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/ata-TS512GMTS430S_J478260466";
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
            hostname = "borg-2";
            system = "x86_64-linux";
            modules = [
              ./nix/modules/samba/server.nix
              ({...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-WD_BLACK_SN770_1TB_23011J801757";
                disko.longhornDevice = "/dev/disk/by-id/nvme-TEAM_TM8FFD004T_TPBF2404020050100710";
                system.stateVersion = "25.05";
                systemd.network.networks."10-ethernet-static" = {
                  matchConfig = {
                    Type = "ether";
                    Kind = "!*"; # exclude all "special" network devices, e.g. tunnel, bridge, virtual.
                  };
                  networkConfig = {
                    Address = "10.69.80.12/25";
                    Gateway = ["10.69.80.1"];
                  };
                };
                hardware.cpu.intel.updateMicrocode = true;
                environment.systemPackages = [
                  (inputs.lenovo_sa120_fanspeed.packages.x86_64-linux.default)
                ];

                services.k3s = {
                  role = "server";
                  serverAddr = "https://10.69.80.10:6443";
                };
              })
            ];
          }
          {
            hostname = "borg-3";
            system = "x86_64-linux";
            modules = [
              ({...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-APPLE_SSD_AP0512N_C02949500NYNGJ21Q";
                disko.longhornDevice = "/dev/disk/by-id/nvme-ADATA_SX8200PNP_2K46292842UU";
                system.stateVersion = "25.11";

                systemd.network.networks."10-ethernet-static" = {
                  matchConfig = {
                    Type = "ether";
                    Kind = "!*"; # exclude all "special" network devices, e.g. tunnel, bridge, virtual.
                  };
                  networkConfig = {
                    Address = "10.69.80.13/25";
                    Gateway = ["10.69.80.1"];
                  };
                };

                services.k3s = {
                  role = "server";
                  serverAddr = "https://10.69.80.10:6443";
                };
              })
            ];
          }
        ];
      in {
        # enable magic rollback and other checks
        checks = builtins.mapAttrs (_: deployLib: deployLib.deployChecks self.deploy) inputs.deploy-rs.lib;
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
                };
                modules =
                  [
                    ./nix/hosts/common
                    (./nix/hosts + "/${host.hostname}")
                    ./nix/modules/common
                    ./nix/modules/restic
                    ./nix/modules/k3s
                    ./nix/modules/users/defaultUser.nix
                    inputs.nixos-facter-modules.nixosModules.facter
                    {
                      defaultUsername = "tkennedy";
                      networking.hostName = host.hostname;
                      # Pin nixpkgs to flake input
                      nix.registry.nixpkgs.flake = inputs.nixpkgs;
                      facter.reportPath = let
                        facterPath = ./nix/hosts + "/${host.hostname}" + /facter.json;
                      in
                        if builtins.pathExists facterPath
                        then facterPath
                        else throw "Have you forgotten to run nixos-anywhere with `--generate-hardware-config nixos-facter ${facterPath}`?";
                      sops.defaultSopsFile = let
                        defaultSopsPath = ./nix/hosts + "/${host.hostname}" + /secrets.yaml;
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
                "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix"
                ({
                  lib,
                  pkgs,
                  ...
                }: {
                  users.users.root.openssh.authorizedKeys.keyFiles = builtins.map (s: ./nix/modules/users/authorized_keys + "/${s}") (builtins.attrNames (builtins.readDir ./nix/modules/users/authorized_keys));
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
