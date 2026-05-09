# Adds kernel mTLS (xprtsec=mtls) on top of an existing NFS server/client setup.
# Requires: kTLS kernel support (kernel >= 5.15), ktls-utils (tlshd), and a step-ca ACME provisioner.
# See docs/nfs-mtls.md for setup steps, PVC examples, and troubleshooting.
#
# Challenge type:
#   HTTP-01 (default): step-ca reaches this host on port 80. Requires a public or internal
#     DNS A record for the hostname and port 80 open (done automatically when dnsProvider is null).
#     environmentFile must contain: EAB_KID, EAB_HMAC_KEY, LEGO_CA_CERTIFICATES
#   DNS-01: lego updates DNS records via a provider API. No port 80 needed, but requires
#     cloud API credentials in environmentFile (e.g. CF_DNS_API_TOKEN for Cloudflare).
{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.nfs-mtls;
  certDir = "/var/lib/acme/${cfg.hostname}";
  useHTTP01 = cfg.acme.dnsProvider == null;
in {
  options.services.nfs-mtls = {
    enable = lib.mkEnableOption "NFS kernel mTLS via tlshd and step-ca ACME";

    serverMode = lib.mkEnableOption "server-mode: add xprtsec=mtls NFS exports and configure tlshd as NFS server";
    clientMode = lib.mkEnableOption "client-mode: configure tlshd so kernel NFS mounts can use xprtsec=mtls";

    hostname = lib.mkOption {
      type = lib.types.str;
      description = "FQDN for this host's TLS certificate";
      example = "borg-2.msng.to";
    };

    acme = {
      server = lib.mkOption {
        type = lib.types.str;
        default = "https://ca.msng.to/acme/nixos/directory";
        description = "step-ca ACME directory URL. Change 'nixos' if you used a different provisioner name.";
      };

      email = lib.mkOption {
        type = lib.types.str;
        default = "terrance@missingtoken.net";
        description = "Email address for ACME account registration.";
      };

      dnsProvider = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          lego DNS provider name for DNS-01 challenge (e.g. "cloudflare").
          When null (the default), HTTP-01 challenge is used instead: lego binds port 80 on
          this host and step-ca validates by fetching the challenge over HTTP. This requires
          the coredns-custom ConfigMap in kube-system to be deployed so step-ca can resolve
          this host's FQDN to its internal LAN IP.
        '';
        example = "cloudflare";
      };

      environmentFile = lib.mkOption {
        type = lib.types.path;
        description = ''
          Path to a systemd EnvironmentFile for lego.
          Required for all challenge types:
            EAB_KID              - External Account Binding key ID (from: step ca acme eab create nixos)
            EAB_HMAC_KEY         - External Account Binding HMAC key
            LEGO_CA_CERTIFICATES - Filesystem path to the step-ca root CA PEM

          Additional key required only for DNS-01 (e.g. Cloudflare):
            CF_DNS_API_TOKEN     - Cloudflare API token with DNS:Edit on the target zone

          Store this as a sops secret and pass config.sops.secrets.<name>.path here.
        '';
        example = "/run/secrets/nfs_acme_env";
      };

      caCertFile = lib.mkOption {
        type = lib.types.path;
        description = ''
          Path to the step-ca root CA certificate PEM used as the tlshd truststore.
          This should be the same file referenced by LEGO_CA_CERTIFICATES in environmentFile.
          Store as a sops secret and pass config.sops.secrets.<name>.path here.
        '';
        example = "/run/secrets/step_ca_root";
      };
    };
  };

  config = lib.mkIf cfg.enable {
    # kTLS kernel module is required for NFS-over-TLS
    boot.kernelModules = ["tls"];

    # Acquire a TLS certificate from step-ca via ACME.
    security.acme = {
      acceptTerms = true;
      certs.${cfg.hostname} =
        {
          server = cfg.acme.server;
          email = cfg.acme.email;
          environmentFile = cfg.acme.environmentFile;
          # Reload tlshd when the cert renews so it uses the new cert/key on the next handshake
          reloadServices = ["tlshd.service"];
        }
        // lib.optionalAttrs useHTTP01 {
          # HTTP-01: lego binds port 80 for the duration of the challenge.
          # Requires the host to have a DNS A record so step-ca pods can resolve this hostname.
          listenHTTP = ":80";
        }
        // lib.optionalAttrs (!useHTTP01) {
          dnsProvider = cfg.acme.dnsProvider;
        };
    };

    # Open port 80 only when using HTTP-01; it's only bound during renewal but must be
    # reachable from step-ca pods in the cluster for the challenge to pass.
    networking.firewall.allowedTCPPorts = lib.optional useHTTP01 80;

    # tlshd: userspace TLS handshake daemon invoked by the kernel for NFS-over-TLS connections.
    # Uses the modular-services pattern shipped with pkgs.ktls-utils.
    system.services.tlshd = {
      imports = [pkgs.ktls-utils.services.default];
      tlshd.settings =
        lib.optionalAttrs cfg.serverMode {
          "authenticate.server" = {
            "x509.certificate" = "${certDir}/cert.pem";
            "x509.private_key" = "${certDir}/key.pem";
            # Trust NFS client certs signed by step-ca
            "x509.truststore" = cfg.acme.caCertFile;
          };
        }
        // lib.optionalAttrs cfg.clientMode {
          "authenticate.client" = {
            "x509.certificate" = "${certDir}/cert.pem";
            "x509.private_key" = "${certDir}/key.pem";
            # Trust the NFS server cert signed by step-ca
            "x509.truststore" = cfg.acme.caCertFile;
          };
        };
    };

    # Persist ACME cert state across reboots. /cache survives reboots but is not backed up;
    # certs can always be re-obtained from step-ca if lost.
    environment.persistence."/cache".directories = [
      "/var/lib/acme"
    ];

    # Server-specific: export /storage/nas/k8s with xprtsec=mtls enforced.
    # xprtsec=mtls in the export entry means the NFS server REQUIRES mTLS for this path —
    # clients connecting without a valid cert signed by step-ca are rejected.
    # The root /storage/nas export (for longhorn) is managed by longhorn-backups.nix and
    # remains accessible without TLS to avoid disrupting existing longhorn backup mounts.
    services.nfs.server.exports = lib.mkIf cfg.serverMode ''
      /storage/nas/k8s 10.69.80.0/25(rw,async,no_subtree_check,no_root_squash,insecure,xprtsec=mtls) 10.42.0.0/16(rw,async,no_subtree_check,no_root_squash,insecure,xprtsec=mtls)
    '';

    # Pre-create the share root directories that the CSI driver uses as mount points.
    # The CSI driver creates per-PV subdirectories *within* these dirs (e.g. /storage/nas/k8s/<uuid>),
    # so the parent must exist before any PVC is provisioned.
    systemd.tmpfiles.rules = lib.mkIf cfg.serverMode [
      "d /storage/nas/k8s 0755 root root -"
      "d /storage/nas/k8s/backups 0755 root root -"
    ];
  };
}
