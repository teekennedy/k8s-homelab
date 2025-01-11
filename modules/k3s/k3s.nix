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

  # Enable graceful shutdown
  # https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/#graceful-node-shutdown
  # Note that due to a regression in local-path-provisioner, this won't work with k3s versions 1.30.x up to 1.31.2.
  services.k3s.gracefulNodeShutdown.enable = true;

  # file permissions and path for k3s token set to match k3s default
  # https://docs.k3s.io/cli/token
  sops.secrets.k3s_token = {
    mode = "0600";
    owner = config.users.users.root.name;
    group = config.users.users.root.group;
    restartUnits = ["k3s.service"];
  };

  services.k3s.tokenFile = config.sops.secrets.k3s_token.path;

  sops.secrets.kubeconfig = lib.mkIf (builtins.pathExists ./kubeconfig.enc.yaml) {
    sopsFile = ./kubeconfig.enc.yaml;
    mode = "0600";
    owner = config.users.users.root.name;
    group = config.users.users.root.group;
    restartUnits = ["k3s.service"];
    key = "";
    path = "/etc/rancher/k3s/k3s.yaml";
  };
}
