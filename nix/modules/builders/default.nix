# Configures distributed Nix builds and SSH-based store sharing across builder clusters.
#
# Peer host data comes from the flake-parts builderClusters registry (injected via
# _module.args.builderClusters), avoiding cross-nixosConfiguration evaluation cycles.
#
# There are three builder roles:
#
#   nix.builders.cluster                — provider: accept builds from peers
#   nix.builders.remoteClusters         — consumer: hand builds *to* peers
#   nix.builders.substituteFromClusters — consumer: fetch already-built paths from peers
#
# A cluster with members that both accept and forward builds (the first two
# options) leads to an infinite loop of build forwarding. Members of a build
# cluster that substitute for one another (options 1 and 3) do not have this
# problem. If a cluster member wants a package that a peer has already built,
# it will download it from the peer. Otherwise, the cluster member that wants
# the package will be the one building it. Cluster peers are prioritized over
# public caches, since local network transfers are faster.
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

  # Every host of the named clusters except this one. Merging first and
  # filtering once means a host listed in two clusters is still one entry.
  peersOf = clusterNames:
    lib.filterAttrs (name: _: name != config.networking.hostName)
    (lib.foldl' (acc: clusterName: acc // (hostsInCluster clusterName)) {} clusterNames);

  buildHosts = peersOf cfg.remoteClusters;
  substituterHosts = peersOf cfg.substituteFromClusters;

  # Both roles reach their peers over the same ssh config, so it is emitted once
  # for the union rather than once per role -- two mkIf branches each writing a
  # `Host` block for the same address would duplicate it in ssh_config.
  sshHosts = buildHosts // substituterHosts;

  isClusterMember = cfg.cluster != null;

  # Lower number wins. cache.nixos.org advertises 40, so this puts a peer on the
  # LAN ahead of the public cache for any path both of them have, which is the
  # point: the transfer is local and the path is already signed by the cluster
  # key. A peer that is down costs a connect attempt bounded by the
  # ConnectTimeout in the ssh config below, not a hang.
  peerPriority = 30;

  peerStore = host: "ssh-ng://nixbuilder@${host.address}?priority=${toString peerPriority}";
  peerStores = hosts: lib.mapAttrsToList (_: peerStore) hosts;
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
      description = ''
        Builder clusters to hand builds to. Setting this on a host that is
        itself a cluster member of the same cluster makes the members offer
        work to each other in a ring; see the note at the top of this file.
      '';
    };

    substituteFromClusters = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = cfg.remoteClusters;
      defaultText = lib.literalExpression "config.nix.builders.remoteClusters";
      description = ''
        Builder clusters whose members are used as binary caches. Unlike
        remoteClusters this is safe to point at a host's own cluster: a
        substituter is only ever asked for paths it already has, so peers
        cannot bounce work between each other.

        Members must be signing their store paths for this to be usable, which
        is what the provider role's secret-key-files setting below does.

        Defaults to remoteClusters, so a consumer that only sets that keeps
        both behaviours.
      '';
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

    # Consumer: hand builds to peers.
    (lib.mkIf (buildHosts != {}) {
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
          buildHosts);
    })

    # Consumer: fetch already-built paths from peers.
    #
    # trusted-substituters as well as extra-substituters because an unprivileged
    # user's --substituters flag is only honoured for stores on that list; the
    # units that matter here run as root, but leaving it off would make the two
    # settings disagree for no reason.
    (lib.mkIf (substituterHosts != {}) {
      nix.settings.extra-substituters = peerStores substituterHosts;
      nix.settings.trusted-substituters = peerStores substituterHosts;
    })

    # One ssh config for both consumer roles.
    (lib.mkIf (sshHosts != {} && cfg.sshKeyFile != null) {
      programs.ssh.extraConfig = lib.concatStringsSep "\n" (lib.mapAttrsToList (_: host: ''
          Host ${host.address}
            User nixbuilder
            IdentityFile ${cfg.sshKeyFile}
            StrictHostKeyChecking no
            ConnectTimeout 5
        '')
        sshHosts);
    })
  ];
}
