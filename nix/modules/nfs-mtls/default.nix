# NFS kernel mTLS (xprtsec=mtls, RFC 9289).
#
# Certificates are issued by cert-manager (internal-ca ClusterIssuer) and synced
# to the host by the nfs-mtls-cert-sync DaemonSet in the cert-system namespace.
# tlshd reads cert/key/ca from /var/lib/nfs-mtls/ at handshake time — cert rotation
# is picked up automatically on the next NFS connection without restarting anything.
#
# tlshd runs as a static system user (not DynamicUser) so that the nfs-mtls-cert-sync
# DaemonSet can reliably chown the private key to tlshd regardless of boot order.
#
# See docs/nfs-mtls.md for architecture details, PVC examples, and troubleshooting.
{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.nfs-mtls;
  certDir = "/var/lib/nfs-mtls";
in {
  options.services.nfs-mtls = {
    enable = lib.mkEnableOption "NFS kernel mTLS via tlshd";

    serverMode = lib.mkEnableOption "server-mode: export /storage/nas/k8s with xprtsec=mtls and configure tlshd as NFS server";

    clientMode = lib.mkEnableOption "client-mode: configure tlshd so kernel NFS mounts can use xprtsec=mtls";
  };

  config = lib.mkIf cfg.enable {
    # kTLS kernel module required for NFS-over-TLS
    boot.kernelModules = ["tls"];

    # Static system user for tlshd so the nfs-mtls-cert-sync DaemonSet can chown
    # the private key to a predictable user regardless of service start order.
    users.users.tlshd = {
      isSystemUser = true;
      group = "tlshd";
      description = "tlshd NFS kernel TLS handshake daemon";
    };
    users.groups.tlshd = {};

    # NFSv4-only: disable v2/v3 while still allowing the default nfs-server unit
    # dependencies (rpcbind/mountd) to start so nfs-server can come up cleanly.
    services.nfs.server.enable = true;
    services.nfs.settings.nfsd = {
      vers2 = "n";
      vers3 = "n";
      vers4 = "y";
      "vers4.1" = "y";
      "vers4.2" = "y";
    };

    networking.firewall.allowedTCPPorts = [2049];

    # tlshd: userspace TLS handshake daemon invoked by the kernel for NFS-over-TLS connections.
    # Certs are written to certDir by the nfs-mtls-cert-sync DaemonSet and read at handshake time.
    # DynamicUser is disabled in favour of the static tlshd user defined above.
    system.services.tlshd = {
      imports = [pkgs.ktls-utils.services.default];
      tlshd.settings =
        lib.optionalAttrs cfg.serverMode {
          "authenticate.server" = {
            "x509.certificate" = "${certDir}/cert.pem";
            "x509.private_key" = "${certDir}/key.pem";
            "x509.truststore" = "${certDir}/ca.crt";
          };
        }
        // lib.optionalAttrs cfg.clientMode {
          "authenticate.client" = {
            "x509.certificate" = "${certDir}/cert.pem";
            "x509.private_key" = "${certDir}/key.pem";
            "x509.truststore" = "${certDir}/ca.crt";
          };
        };
    };

    # Override DynamicUser so the static tlshd user above is used instead.
    systemd.services.tlshd.serviceConfig = {
      DynamicUser = lib.mkForce false;
      User = "tlshd";
      Group = "tlshd";
    };

    # Export /storage/nas with xprtsec=mtls enforced, node LAN only.
    # Pod CIDR is intentionally excluded from the root export: kernel NFS mounts go
    # through the CSI node plugin (node IP), so broad pod-level access is neither
    # needed nor permitted.
    services.nfs.server.exports = lib.mkIf cfg.serverMode ''
      /storage/nas 10.69.80.0/25(rw,async,fsid=0,insecure,no_subtree_check,no_root_squash,pnfs,xprtsec=mtls)
    '';

    # Pre-create the root directory used by the CSI driver for per-PV subdirectories.
    systemd.tmpfiles.rules = lib.mkIf cfg.serverMode [
      "d /storage/nas/k8s 0755 root root -"
    ];
  };
}
