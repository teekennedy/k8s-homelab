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
        k3s
        kubectl
      ]
      ++ lib.optionals isServer [
        etcd.deps.etcdctl # having etcdctl is helpful when you need to manage the cluster
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
      tokenFile = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) config.sops.secrets.k3s_token.path;
    };

    # Enable graceful shutdown
    # https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown
    # Note that due to a regression in local-path-provisioner, this won't work with k3s versions 1.30.x up to 1.31.2.
    services.k3s.gracefulNodeShutdown.enable = true;
    services.k3s.gracefulNodeShutdown.shutdownGracePeriodCriticalPods = "60s";
    services.k3s.gracefulNodeShutdown.shutdownGracePeriod = "120s";

    # Persist k3s server datastore (etcd) across reboots
    environment.persistence."/persistent".directories = lib.mkIf isServer [
      "/var/lib/rancher/k3s/server/db"
    ];

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
