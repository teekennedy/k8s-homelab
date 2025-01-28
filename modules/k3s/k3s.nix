{
  config,
  inputs,
  lib,
  pkgs,
  ...
}: {
  networking.firewall.allowedTCPPorts = [
    6443 # k3s: required so that pods can reach the API server (running on port 6443 by default)
    2379 # k3s, etcd clients: required if using a "High Availability Embedded etcd" configuration
    2380 # k3s, etcd peers: required if using a "High Availability Embedded etcd" configuration
  ];
  networking.firewall.allowedUDPPorts = [
    8472 # k3s, flannel: required if using multi-node for inter-node networking
  ];
  environment.systemPackages = with pkgs; [
    k3s
    kubectl
    etcd # having etcdctl is helpful when you need to manage the cluster
  ];

  services.k3s = {
    enable = lib.mkDefault true;
    # enable secrets encryption
    # This _must_ be enabled when cluster is first initialized. It cannot be enabled later.
    # https://docs.k3s.io/cli/secrets-encrypt
    extraFlags = ["--secrets-encryption"];
  };

  # Enable graceful shutdown
  # https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown
  # Note that due to a regression in local-path-provisioner, this won't work with k3s versions 1.30.x up to 1.31.2.
  services.k3s.gracefulNodeShutdown.enable = true;

  # Setup secrets
  sops.secrets = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) (let
    sopsConfig = {
      sopsFile = ./secrets.enc.yaml;
      mode = "0600";
      owner = config.users.users.root.name;
      group = config.users.users.root.group;
    };
  in {
    # file permissions and path for k3s token set to match k3s default
    # https://docs.k3s.io/cli/token
    k3s_token =
      sopsConfig
      // {
        path = "/var/lib/rancher/k3s/server/token";
      };
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
}
