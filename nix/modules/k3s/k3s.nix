{
  config,
  lib,
  pkgs,
  ...
}: let
  isServer = config.services.k3s.role != "agent";
in {
  options = {
    services.k3s.embeddedRegistry.enable = lib.mkOption {
      description = "Whether to enable the k3s embedded registry mirror. Configured via the registries_yaml secret in secrets.enc.yaml.  https://docs.k3s.io/installation/registry-mirror";
      default = true;
      type = lib.types.bool;
    };
  };
  config = {
    # Only update k3s one minor version at a time
    services.k3s.package = pkgs.k3s_1_37;
    # https://docs.k3s.io/installation/requirements#inbound-rules-for-k3s-nodes
    networking.firewall.allowedTCPPorts = lib.mkMerge [
      [
        # prometheus node exporter
        9100
        # MetalLB speaker memberlist gossip
        7946
        # Kube proxy metrics
        10249
        # Kubelet metrics
        10250
      ]
      (lib.mkIf isServer [
        # kubernetes API server
        6443
        # k3s, etcd clients: required if using a "High Availability Embedded etcd" configuration
        2379
        # k3s, etcd peers: required if using a "High Availability Embedded etcd" configuration
        2380
        # k3s etcd metrics: if using flag --etcd-expose-metrics=true
        2381
        # Kube controller manager metrics
        10257
        # Kube scheduler metrics
        10259
      ])
      (lib.mkIf config.services.k3s.embeddedRegistry.enable [
        # k3s embedded registry mirror
        5001
      ])
    ];
    networking.firewall.allowedUDPPorts = [
      # MetalLB speaker memberlist gossip
      7946
    ];
    environment.systemPackages = with pkgs;
      [
        kubectl
      ]
      ++ lib.optionals isServer [
        # Etcd 3.7 package has no passthru deps as of 2026-10-22. Fallback to 3.6 for now.
        # TODO switch to etcd 3.7 etcdctl once its available through passthru deps.
        # See https://github.com/NixOS/nixpkgs/blob/nixos-unstable/pkgs/by-name/et/etcd_3_7/package.nix
        etcd_3_6.deps.etcdctl # having etcdctl is helpful when you need to manage the cluster
      ];

    # Increase the inotify user instance limit.
    # The default of 128 can cause pods to fail.
    boot.kernel.sysctl."fs.inotify.max_user_instances" = 8192;

    services.k3s = {
      enable = lib.mkDefault true;
      extraFlags = lib.mkMerge [
        [
          # Tell k3s to use systemd-resolved's generated resolv.conf file
          "--resolv-conf"
          "/run/systemd/resolve/resolv.conf"
          # Enable the kube-proxy metrics endpoint
          "--kube-proxy-arg"
          "metrics-bind-address=0.0.0.0"
        ]
        (lib.mkIf isServer [
          # Route pod subnets through node IPs using layer 2 routing.
          # Better performance and fewer dependencies than default vxlan backend
          "--flannel-backend=host-gw"
          # Disable k3s built-in Traefik; managed by ArgoCD instead
          "--disable"
          "traefik"
          # Disable k3s built-in ServiceLB; MetalLB handles all load balancing
          "--disable"
          "servicelb"
          # Add the MetalLB API VIP to the API server TLS certificate
          "--tls-san"
          "10.69.80.101"
          # enable secrets encryption
          # This _must_ be enabled when cluster is first initialized. It cannot be enabled later.
          # https://docs.k3s.io/cli/secrets-encrypt
          "--secrets-encryption"
          # Enable etcd metrics endpoint
          "--etcd-expose-metrics=true"
          # Commit etcd's bbolt backend once a second instead of every 100ms.
          # Lease renewals kept it committing ~5x/s, two fsyncs each, and each
          # fsync on btrfs also writes tree-log and metadata blocks. The WAL
          # is still fsynced per write, so durability is unchanged.
          "--etcd-arg=backend-batch-interval=1s"
          # Enable the kube-controller metrics endpoint
          "--kube-controller-manager-arg"
          "bind-address=0.0.0.0"
          # Enable the kube-scheduler metrics endpoint
          "--kube-scheduler-arg"
          "bind-address=0.0.0.0"
        ])
        (lib.mkIf (isServer && config.services.k3s.embeddedRegistry.enable) [
          "--embedded-registry"
        ])
      ];
      nodeLabel = lib.mkIf (builtins.stringLength config.disko.longhornDevice > 0) [
        "node.longhorn.io/create-default-disk=true"
        "node.msng.to/cnpg-storage=true"
      ];
      tokenFile = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) config.sops.secrets.k3s_token.path;
    };

    # Graceful node shutdown, stopping pods in priority tiers from lowest up.
    # kubelet waits for a tier's volumes to unmount before starting the next,
    # so Longhorn (longhorn-critical, 1000000000) keeps serving app volumes
    # until they are unmounted. The two-bucket gracefulNodeShutdown options
    # treat longhorn-critical as non-critical and stop it alongside the apps.
    # Mutually exclusive with services.k3s.gracefulNodeShutdown.
    # https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#pod-priority-graceful-node-shutdown
    # Note that due to a regression in local-path-provisioner, this won't work with k3s versions 1.30.x up to 1.31.2.
    services.k3s.extraKubeletConfig.shutdownGracePeriodByPodPriority = [
      {
        priority = 0;
        shutdownGracePeriodSeconds = 60;
      }
      {
        priority = 1000000000;
        shutdownGracePeriodSeconds = 30;
      }
      {
        priority = 2000000000;
        shutdownGracePeriodSeconds = 15;
      }
    ];

    # Persist important k3s data across reboots
    environment.persistence."/persistent" = {
      directories = lib.optionals isServer [
        # etcd store
        "/var/lib/rancher/k3s/server/db"
        # node password, secrets encryption key, and other credentials
        "/var/lib/rancher/k3s/server/cred"
      ];

      files = lib.optionals (isServer == false) [
        # k3s agent node password
        "/etc/rancher/node/password"
      ];
    };

    # etcd rewrites bbolt pages in place several times a second. On
    # copy-on-write btrfs each rewrite relocates the extent, fragmenting the
    # db file (~9k extents vs ~1k with +C). nodatacow (+C) keeps the writes
    # in place; etcd checksums its own WAL and snapshots. It does not reduce
    # write volume much: that is dominated by the metadata btrfs writes on
    # every fsync, which nodatacow files still pay.
    #
    # +C only takes effect on files created empty inside a +C directory, so
    # existing files are copied into place. That only happens while k3s is
    # stopped, normally at boot; otherwise just the directories are marked and
    # etcd's own WAL rotation and defrags pick it up over time.
    # Snapshots under db/snapshots are left alone so they stay compressed.
    systemd.services.k3s-etcd-nocow = lib.mkIf isServer {
      description = "Disable btrfs copy-on-write for the k3s etcd data dir";
      wantedBy = ["k3s.service"];
      before = ["k3s.service"];
      unitConfig.RequiresMountsFor = ["/var/lib/rancher/k3s/server/db"];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      path = with pkgs; [coreutils e2fsprogs findutils gawk systemd util-linux];
      script = ''
        dir=/var/lib/rancher/k3s/server/db/etcd
        if [ ! -d "$dir" ]; then
          echo "$dir does not exist yet; nothing to do"
          exit 0
        fi

        if [ "$(stat -f -c %T "$dir")" != btrfs ]; then
          echo "$dir is not on btrfs; nothing to do"
          exit 0
        fi

        find "$dir" -type d -exec chattr +C {} +

        db="$dir/member/snap/db"
        if systemctl is-active --quiet k3s.service \
          || { [ -f "$db" ] && ! flock --nonblock --exclusive "$db" true; }; then
          echo "etcd is running; only marked directories +C"
          exit 0
        fi

        # Left behind if a previous run was interrupted mid-copy.
        find "$dir" -type f -name '*.nocow.tmp' -delete

        find "$dir" -type f -print0 | while IFS= read -r -d "" f; do
          if lsattr -d -- "$f" | awk '{ exit ($1 ~ /C/) ? 0 : 1 }'; then
            continue
          fi
          tmp="$f.nocow.tmp"
          rm -f -- "$tmp"
          # --reflink=never: a reflinked copy would share the old COW extents.
          # --sparse=never: keeps etcd's preallocated WAL files allocated.
          cp --reflink=never --sparse=never \
            --preserve=mode,ownership,timestamps -- "$f" "$tmp"
          sync -- "$tmp"
          mv -f -- "$tmp" "$f"
          echo "rewrote $f without copy-on-write"
        done
        sync -f "$dir"
      '';
    };

    # Setup secrets
    sops.secrets = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) (let
      sopsConfig = {
        sopsFile = ./secrets.enc.yaml;
        mode = "0600";
        owner = config.users.users.root.name;
        group = config.users.users.root.group;
      };
    in
      {
        # k3s token needed by both server and agent nodes to join the cluster
        # https://docs.k3s.io/cli/token
        k3s_token =
          sopsConfig
          // {
            # file permissions and path for k3s token set to match k3s default
            path =
              if isServer
              then "/var/lib/rancher/k3s/server/token"
              else "/var/lib/rancher/k3s/agent/token";
          };
        registries_yaml =
          sopsConfig
          // {
            path = "/etc/rancher/k3s/registries.yaml";
          };
      }
      // lib.optionalAttrs isServer {
        server_ca_crt =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/server-ca.crt";
          };
        server_ca_key =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/server-ca.key";
          };
        client_ca_crt =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/client-ca.crt";
            mode = "0644";
          };
        client_ca_key =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/client-ca.key";
          };
        request_header_ca_crt =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/request-header-ca.crt";
            mode = "0644";
          };
        request_header_ca_key =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/request-header-ca.key";
          };
        etcd_peer_ca_crt =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/etcd/peer-ca.crt";
            mode = "0644";
          };
        etcd_peer_ca_key =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/etcd/peer-ca.key";
          };
        etcd_server_ca_crt =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/etcd/server-ca.crt";
            mode = "0644";
          };
        etcd_server_ca_key =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/etcd/server-ca.key";
          };
        service_key =
          sopsConfig
          // {
            path = "/var/lib/rancher/k3s/server/tls/service.key";
          };
      });
  };
}
