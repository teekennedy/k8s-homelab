{
  description = "teekennedy's homelab";
  inputs = {
    deploy-rs.url = "github:serokell/deploy-rs?ref=master";
    deploy-rs.inputs.nixpkgs.follows = "nixpkgs";
    determinate.url = "github:DeterminateSystems/determinate";
    nixos-facter-modules.url = "github:nix-community/nixos-facter-modules?ref=main";
    # TEMPORARY: pinned to PR #540857 (KiaraGrouwstra/nixpkgs@modular-reload-fixes)
    # which fixes a modular-services (system.services.*) eval regression that
    # otherwise breaks tlshd / nix/modules/nfs-mtls on every borg host. Revert to
    # "github:NixOS/nixpkgs/nixos-unstable" once the PR lands in nixos-unstable.
    # NB: this branch is based on nixpkgs master, not nixos-unstable.
    nixpkgs.url = "github:KiaraGrouwstra/nixpkgs/ae9994806ca939447a8a6adc4f94d12cbdf06e01";
    disko.url = "github:nix-community/disko?ref=master";
    disko.inputs.nixpkgs.follows = "nixpkgs";
    impermanence.url = "github:nix-community/impermanence?ref=master";
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
      imports = [./nix/modules/builders/flake-module.nix];
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
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-Samsung_SSD_9100_PRO_1TB_S7YENS0L104680K";
                disko.longhornDevice = "/dev/disk/by-id/nvme-TEAM_TM8FP4004T_112302210210813";
                system.stateVersion = "26.11";
                services.k3s = {
                  role = "server";
                  serverAddr = "https://10.69.80.101:6443";
                };

                hardware.cpu.intel.updateMicrocode = true;
              })
            ];
          }
          {
            hostname = "borg-1";
            system = "x86_64-linux";
            modules = [
              ({...}: {
                disko.devices.disk.main.device = "/dev/disk/by-id/nvme-WD_BLACK_SN770_1TB_23011J802382";
                system.stateVersion = "26.05";
                systemd.network.networks."10-ethernet-static" = {
                  matchConfig = {
                    Type = "ether";
                    Kind = "!*"; # exclude all "special" network devices, e.g. tunnel, bridge, virtual.
                  };
                  networkConfig = {
                    Address = "10.69.80.11/25";
                    Gateway = ["10.69.80.1"];
                  };
                };
                hardware.cpu.intel.updateMicrocode = true;

                services.k3s = {
                  role = "server";
                  serverAddr = "https://10.69.80.101:6443";
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
                  # Cluster init node is responsible for bootstrapping the cluster
                  clusterInit = true;
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

                hardware.cpu.intel.updateMicrocode = true;

                services.k3s = {
                  role = "agent";
                  serverAddr = "https://10.69.80.101:6443";
                };
              })
            ];
          }
        ];
      in {
        # Addresses and build capacity for each borg builder host.
        # maxJobs defaults to half the CPU core count read from each host's facter report.
        builderClusters.borg = {
          "borg-0" = {
            address = "10.69.80.10";
            facterPath = ./nix/hosts/borg-0/facter.json;
          };
          "borg-1" = {
            address = "10.69.80.11";
            facterPath = ./nix/hosts/borg-1/facter.json;
          };
          "borg-2" = {
            address = "10.69.80.12";
            facterPath = ./nix/hosts/borg-2/facter.json;
          };
          "borg-3" = {
            address = "10.69.80.13";
            facterPath = ./nix/hosts/borg-3/facter.json;
          };
        };

        # enable magic rollback and other checks
        checks = builtins.mapAttrs (_: deployLib: deployLib.deployChecks self.deploy) inputs.deploy-rs.lib;
        deploy = {
          nodes = builtins.listToAttrs (map (host: {
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
          # Build on remote host
          remoteBuild = true;
        };
        nixosConfigurations =
          (builtins.listToAttrs (map (host: {
              name = host.hostname;
              value = inputs.nixpkgs.lib.nixosSystem {
                system = host.system;
                specialArgs = {
                  inherit inputs self;
                };
                modules =
                  [
                    self.nixosModules.builders
                    {
                      # Builder provider only. Accept builds but do not
                      # forward them, preventing infinite forwarding loops.
                      nix.builders = {
                        cluster = "borg";
                        remoteClusters = [];
                      };
                    }
                    ./nix/hosts/common
                    (./nix/hosts + "/${host.hostname}")
                    ./nix/modules/common
                    ./nix/modules/restic
                    ./nix/modules/k3s
                    ./nix/modules/nfs-mtls
                    ./nix/modules/users/defaultUser.nix
                    inputs.determinate.nixosModules.default
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
            # Live NixOS distribution used to bootstrap a host.
            # See docs/nix-host-bootstrap.md for more info.
            installIso = inputs.nixpkgs.lib.nixosSystem {
              system = "x86_64-linux";
              specialArgs = {inherit inputs self;};
              modules = [
                "${inputs.nixpkgs}/nixos/modules/installer/cd-dvd/installation-cd-minimal.nix"
                # Nix caches and settings
                ./nix/modules/common/nix.nix
                # Use borg hosts as remote builders when building with this ISO
                self.nixosModules.builders
                ({
                  lib,
                  pkgs,
                  ...
                }: {
                  nix.builders.remoteClusters = ["borg"];
                  users.users.root.openssh.authorizedKeys.keyFiles = map (s: ./nix/modules/users/authorized_keys + "/${s}") (builtins.attrNames (builtins.readDir ./nix/modules/users/authorized_keys));
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
