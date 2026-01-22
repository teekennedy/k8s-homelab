{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.ci.runner;
  inherit (lib) mkAfter mkEnableOption mkIf mkMerge mkOption types;
in {
  options.ci.runner = {
    enable = mkEnableOption "Gitea/Forgejo actions runner";

    instance = mkOption {
      type = types.str;
      default = config.networking.hostName;
      description = "Runner instance key for systemd services.";
    };

    name = mkOption {
      type = types.str;
      default = cfg.instance;
      description = "Runner name registered with Gitea/Forgejo.";
    };

    url = mkOption {
      type = types.str;
      example = "https://gitea.example.com";
      description = "Base URL of the Gitea or Forgejo instance.";
    };

    token = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = "Registration token for the runner (plain text).";
    };

    tokenFile = mkOption {
      type = types.nullOr (types.either types.path types.str);
      default = null;
      description = "Environment file containing TOKEN for runner registration.";
    };

    labels = mkOption {
      type = types.listOf types.str;
      default = ["native:host"];
      description = "Runner labels to map jobs to runtime environments.";
    };

    settings = mkOption {
      type = types.attrs;
      default = {};
      description = "Runner settings passed to act_runner.";
    };

    capacity = mkOption {
      type = types.nullOr types.int;
      default = null;
      description = "Maximum concurrent jobs handled by the runner.";
    };

    containerOptions = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "Container runtime options (for example, resource limits).";
    };

    hostPackages = mkOption {
      type = types.listOf types.package;
      default = with pkgs; [
        bash
        coreutils
        curl
        gawk
        gitMinimal
        gnused
        nodejs
        wget
      ];
      description = "Packages available to jobs when using host labels.";
    };

    enableDocker = mkOption {
      type = types.bool;
      default = true;
      description = "Enable Docker for container-backed runner labels.";
    };

    enablePodman = mkOption {
      type = types.bool;
      default = false;
      description = "Enable Podman for container-backed runner labels.";
    };

    enableKvm = mkOption {
      type = types.bool;
      default = true;
      description = "Enable KVM access for QEMU-based NixOS tests.";
    };

    extraGroups = mkOption {
      type = types.listOf types.str;
      default = [];
      description = "Extra groups to attach to the runner systemd service.";
    };
  };

  config = mkIf cfg.enable (mkMerge [
    (mkIf cfg.enableDocker {virtualisation.docker.enable = true;})
    (mkIf cfg.enablePodman {virtualisation.podman.enable = true;})
    (mkIf cfg.enableKvm {
      users.groups.kvm = {};
      boot.kernelModules = ["kvm-intel" "kvm-amd"];
    })
    (mkIf (cfg.extraGroups != []) {
      users.groups = lib.genAttrs cfg.extraGroups (_: {});
    })
    {
      services.gitea-actions-runner.instances.${cfg.instance} = {
        enable = true;
        name = cfg.name;
        url = cfg.url;
        token = cfg.token;
        tokenFile = cfg.tokenFile;
        labels = cfg.labels;
        settings = mkMerge [
          cfg.settings
          (mkIf (cfg.capacity != null) {runner.capacity = cfg.capacity;})
          (mkIf (cfg.containerOptions != []) {container.options = cfg.containerOptions;})
        ];
        hostPackages = cfg.hostPackages;
      };
    }
    (mkIf cfg.enableKvm {
      systemd.services."gitea-runner-${cfg.instance}".serviceConfig.SupplementaryGroups =
        mkAfter (["kvm"] ++ cfg.extraGroups);
    })
    (mkIf (!cfg.enableKvm && cfg.extraGroups != []) {
      systemd.services."gitea-runner-${cfg.instance}".serviceConfig.SupplementaryGroups =
        mkAfter cfg.extraGroups;
    })
  ]);
}
