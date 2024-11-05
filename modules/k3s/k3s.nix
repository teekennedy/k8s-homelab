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
  ];

  services.k3s.enable = true;
  services.k3s.role = "server";

  # Enable graceful shutdown
  # https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown
  # Note that due to a regression in local-path-provisioner, this won't work with k3s versions 1.30.x up to 1.31.2.
  services.k3s.gracefulNodeShutdown.enable = true;

  # Bootstrap cluster
  services.k3s.clusterInit = true;
}
