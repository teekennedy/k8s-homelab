# Floating address for the k3s API, held by whichever server's API server is
# ready. keepalived runs on the host, so the address keeps working when the
# cluster itself is broken.
{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.k3s.apiVip;
  env = builtins.fromJSON (builtins.readFile cfg.environmentFile);
  hostName = config.networking.hostName;
  self = lib.findFirst (h: h.name == hostName) null env.hosts;
  servers = builtins.filter (h: h.k3s.role == "server") env.hosts;
  vip = env.cluster.api_vip or null;
  enable = self != null && self.k3s.role == "server" && vip != null;

  tls = "/var/lib/rancher/k3s/server/tls";
  # /readyz covers etcd, so a server cut off from quorum gives the address up.
  readyz = pkgs.writeShellScript "k3s-api-readyz" ''
    exec ${lib.getExe pkgs.curl} --silent --fail --output /dev/null --max-time 2 \
      --cacert ${tls}/server-ca.crt \
      --cert ${tls}/client-admin.crt --key ${tls}/client-admin.key \
      https://127.0.0.1:6443/readyz
  '';
in {
  options.services.k3s.apiVip = {
    environmentFile = lib.mkOption {
      type = lib.types.path;
      default = ../../../config/gen/production/env.json;
      description = "CUE environment export that lists the hosts and cluster.api_vip.";
    };
    interface = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Interface the API address is added to on k3s servers.";
    };
  };

  config = lib.mkMerge [
    # The API address is also on the interface, so k3s must not pick its node
    # address (and etcd's advertise address) by itself.
    (lib.mkIf (self != null) {
      services.k3s.extraFlags = ["--node-ip=${self.ip}"];
    })

    (lib.mkIf enable {
      assertions = [
        {
          assertion = cfg.interface != null;
          message = "services.k3s.apiVip.interface must be set on k3s server ${hostName}.";
        }
      ];

      services.keepalived = {
        enable = true;
        openFirewall = true;
        enableScriptSecurity = true;
        vrrpScripts.k3s_api_ready = {
          script = "${readyz}";
          # Reads the k3s admin client key.
          user = "root";
          interval = 2;
          timeout = 3;
          fall = 2;
          rise = 2;
        };
        vrrpInstances.k3s_api = {
          interface = cfg.interface;
          # All servers start as BACKUP with equal priority and never preempt,
          # so the address only moves when its holder fails the check.
          state = "BACKUP";
          noPreempt = true;
          priority = 100;
          virtualRouterId = lib.toInt (lib.last (lib.splitString "." vip));
          unicastSrcIp = self.ip;
          unicastPeers = map (h: h.ip) (builtins.filter (h: h.name != hostName) servers);
          virtualIps = [{addr = "${vip}/32";}];
          trackScripts = ["k3s_api_ready"];
        };
      };
    })
  ];
}
