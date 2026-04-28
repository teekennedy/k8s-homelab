# Configures distributed Nix builds and SSH-based store sharing across builder clusters.
#
# Peer host data comes from the flake-parts builderClusters registry (injected via
# _module.args.builderClusters), avoiding cross-nixosConfiguration evaluation cycles.
#
# NixOS options:
#   nix.builders.cluster        — cluster this host belongs to (provider role)
#   nix.builders.remoteClusters — clusters to pull builds from (consumer role)
#   nix.builders.sshKeyFile     — SSH key for outgoing connections; auto-set for cluster members
#
# Cluster member setup (one-time per cluster): Run scripts/setup-builders.sh
{
  config,
  lib,
  pkgs,
  inputs,
  builderClusters,
  ...
}: let
  cfg = config.nix.builders;

  hostsInCluster = clusterName: builderClusters.${clusterName} or {};

  remoteHosts =
    lib.foldl'
    (acc: clusterName: acc // (hostsInCluster clusterName))
    {}
    cfg.remoteClusters;

  # All remote hosts from the configured clusters, excluding this host
  peerHosts =
    lib.filterAttrs (name: _: name != config.networking.hostName) remoteHosts;

  isClusterMember = cfg.cluster != null;
  hasRemoteClusters = cfg.remoteClusters != [];
in {
  imports = [inputs.sops-nix.nixosModules.sops];

  options.nix.builders = {
    cluster = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        The builder cluster this host belongs to. When set, this host is
        configured to accept incoming builder connections from cluster peers.
      '';
    };

    remoteClusters = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = "Builder clusters to use for remote builds and substitution.";
    };

    sshKeyFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        Path to the SSH private key used to connect to remote builders.
        Cluster members have this set automatically from their sops-managed
        nixbuilder key. Non-cluster members using remoteClusters must set
        this explicitly to enable SSH-based builds and substitution.
      '';
    };
  };

  config = lib.mkMerge [
    # Provider: configure this host to accept incoming builder connections
    (lib.mkIf isClusterMember {
      sops.secrets.nixbuilder_ssh_key = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) {
        sopsFile = ./secrets.enc.yaml;
        owner = config.users.users.root.name;
        group = config.users.users.root.group;
        mode = "0400";
      };

      sops.secrets.cluster_signing_key = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) {
        sopsFile = ./secrets.enc.yaml;
        owner = config.users.users.root.name;
        group = config.users.users.root.group;
        mode = "0400";
      };

      users.users.nixbuilder = {
        isSystemUser = true;
        group = "nixbuilder";
        shell = pkgs.bashInteractive;
        home = "/var/lib/nixbuilder";
        createHome = true;
        openssh.authorizedKeys.keyFiles = [./nixbuilder_ed25519.pub];
      };
      users.groups.nixbuilder = {};

      nix.settings.trusted-users = ["nixbuilder"];

      # Hardcode the sops-nix secret paths to avoid a circular dependency through
      # config.sops.secrets.*.{owner,group} → users → nix.settings
      nix.builders.sshKeyFile =
        lib.mkIf (builtins.pathExists ./secrets.enc.yaml)
        (lib.mkDefault "/run/secrets/nixbuilder_ssh_key");

      # Sign all locally built store paths so peers can substitute them
      nix.settings.secret-key-files =
        lib.optionals (builtins.pathExists ./secrets.enc.yaml)
        ["/run/secrets/cluster_signing_key"];
    })

    # Trust signed paths from any cluster member (applies to providers and consumers)
    (lib.mkIf (builtins.pathExists ./cluster-signing_ed25519.pub) {
      nix.settings.trusted-public-keys = [
        (lib.removeSuffix "\n" (builtins.readFile ./cluster-signing_ed25519.pub))
      ];
    })

    # Consumer (cluster member): use remote peers as builders via sops key
    (lib.mkIf (isClusterMember && hasRemoteClusters) {
      nix.distributedBuilds = true;
      nix.settings.builders-use-substitutes = true;

      nix.buildMachines =
        lib.optionals (cfg.sshKeyFile != null)
        (lib.mapAttrsToList (_: host: {
            hostName = host.address;
            sshUser = "nixbuilder";
            sshKey = cfg.sshKeyFile;
            systems = [host.system];
            maxJobs = host.maxJobs;
            speedFactor = host.speedFactor;
            supportedFeatures = host.supportedFeatures;
          })
          peerHosts);

      nix.settings.extra-substituters =
        lib.mapAttrsToList (_: host: "ssh-ng://nixbuilder@${host.address}") peerHosts;

      nix.settings.trusted-substituters =
        lib.mapAttrsToList (_: host: "ssh-ng://nixbuilder@${host.address}") peerHosts;

      programs.ssh.extraConfig =
        lib.optionalString (cfg.sshKeyFile != null)
        (lib.concatStringsSep "\n"
          (lib.mapAttrsToList (_: host: ''
              Host ${host.address}
                User nixbuilder
                IdentityFile ${cfg.sshKeyFile}
                StrictHostKeyChecking no
                ConnectTimeout 5
            '')
            peerHosts));
    })

    # Consumer (non-cluster member): use remote peers as builders via explicit key
    (lib.mkIf (!isClusterMember && hasRemoteClusters) {
      nix.distributedBuilds = true;
      nix.settings.builders-use-substitutes = true;

      nix.buildMachines =
        lib.optionals (cfg.sshKeyFile != null)
        (lib.mapAttrsToList (_: host: {
            hostName = host.address;
            sshUser = "nixbuilder";
            sshKey = cfg.sshKeyFile;
            systems = [host.system];
            maxJobs = host.maxJobs;
            speedFactor = host.speedFactor;
            supportedFeatures = host.supportedFeatures;
          })
          peerHosts);

      nix.settings.extra-substituters =
        lib.mapAttrsToList (_: host: "ssh-ng://nixbuilder@${host.address}") peerHosts;

      nix.settings.trusted-substituters =
        lib.mapAttrsToList (_: host: "ssh-ng://nixbuilder@${host.address}") peerHosts;

      programs.ssh.extraConfig =
        lib.optionalString (cfg.sshKeyFile != null)
        (lib.concatStringsSep "\n"
          (lib.mapAttrsToList (_: host: ''
              Host ${host.address}
                User nixbuilder
                IdentityFile ${cfg.sshKeyFile}
                StrictHostKeyChecking no
                ConnectTimeout 5
            '')
            peerHosts));
    })
  ];
}
