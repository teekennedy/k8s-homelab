# NFS kernel mTLS (xprtsec=mtls, RFC 9289).
#
# Certificates are issued by cert-manager (internal-ca ClusterIssuer) and synced
# to the host by the nfs-mtls-cert-sync DaemonSet in the cert-system namespace.
# tlshd reads cert/key/ca from /var/lib/nfs-mtls/ at handshake time — cert rotation
# is picked up automatically on the next NFS connection without restarting anything.
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

    # tlshd: userspace TLS handshake daemon invoked by the kernel for NFS-over-TLS connections.
    # Certs are written to certDir by the nfs-mtls-cert-sync DaemonSet and read at handshake time.
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

    # Export /storage/nas/k8s with xprtsec=mtls enforced. Clients without a valid
    # cert signed by the internal CA are rejected at the NFS level.
    services.nfs.server.exports = lib.mkIf cfg.serverMode ''
      /storage/nas/k8s 10.69.80.0/25(rw,async,no_subtree_check,no_root_squash,insecure,xprtsec=mtls) 10.42.0.0/16(rw,async,no_subtree_check,no_root_squash,insecure,xprtsec=mtls)
    '';

    # Pre-create share root directories used by the CSI driver for per-PV subdirectories.
    systemd.tmpfiles.rules = lib.mkIf cfg.serverMode [
      "d /storage/nas/k8s 0755 root root -"
      "d /storage/nas/k8s/backups 0755 root root -"
    ];
  };
}
