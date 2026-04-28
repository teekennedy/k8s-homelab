{
  config,
  lib,
  ...
}: {
  options.flake.builderClusters = lib.mkOption {
    default = {};
    description = ''
      Registry of builder clusters. Maps cluster name to an attrset of
      hostname → host record. Each host record declares the address, system,
      and optionally a facterPath used to compute the maxJobs default.

      Example:
        flake.builderClusters.borg = {
          "borg-0" = { address = "10.69.80.10"; facterPath = ./nix/hosts/borg-0/facter.json; };
        };
    '';
    type = lib.types.attrsOf (lib.types.attrsOf (lib.types.submodule ({config, ...}: {
      options = {
        address = lib.mkOption {
          type = lib.types.str;
          description = "IP address peers use to connect to this host via SSH.";
        };

        system = lib.mkOption {
          type = lib.types.str;
          default = "x86_64-linux";
          description = "Nix system type of this host.";
        };

        facterPath = lib.mkOption {
          type = lib.types.nullOr lib.types.path;
          default = null;
          description = "Path to a nixos-facter report used to compute the maxJobs default.";
        };

        maxJobs = lib.mkOption {
          type = lib.types.int;
          description = "Max parallel build jobs on this host. Defaults to half its CPU core count.";
        };

        speedFactor = lib.mkOption {
          type = lib.types.int;
          default = 1;
          description = "Relative build speed hint for the Nix scheduler.";
        };

        supportedFeatures = lib.mkOption {
          type = lib.types.listOf lib.types.str;
          default = ["nixos-test" "big-parallel" "kvm"];
          description = "Nix build features supported by this host.";
        };
      };

      config.maxJobs = lib.mkDefault (let
        facterData =
          if config.facterPath != null
          then builtins.fromJSON (builtins.readFile config.facterPath)
          else {};
        cpus = facterData.hardware.cpu or [];
        totalCores = builtins.foldl' (acc: cpu: acc + (cpu.cores or 1)) 0 cpus;
      in
        if totalCores < 2
        then 1
        else builtins.div totalCores 2);
    })));
  };

  # Expose the NixOS module as a flake output. It injects builderClusters into
  # _module.args so that ./default.nix can receive it as a function parameter.
  config.flake.nixosModules.builders = let
    builderClusters = config.flake.builderClusters;
  in
    {...}: {
      _module.args.builderClusters = builderClusters;
      imports = [./default.nix];
    };
}
