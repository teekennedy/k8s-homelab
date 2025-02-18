{
  config,
  lib,
  pkgs,
  ...
}: {
  imports = [./common.nix];
  options = {
    security.krb5.bootstrapped = lib.mkOption {
      description = "Whether the krb5kdc database has been created. Kerberos will refuse to start without it.";
      type = lib.types.bool;
      default = false;
    };
  };
  config = {
    networking.firewall.allowedTCPPorts = [
      111 # rpc
      2049 # nfs
      88 # kerberos
      749 # kerberos admin
    ];
    security.krb5 = {
      package = pkgs.krb5;
    };

    services.kerberos_server.enable = config.security.krb5.bootstrapped;
    services.kerberos_server.settings.realms = lib.mkIf config.security.krb5.bootstrapped {
      "MSNG.TO".acl = [
        {
          access = ["add" "cpw" "get"];
          principal = "admin/admin";
        }
      ];
    };
    services.nfs.server.enable = true;
    services.nfs.server.createMountPoints = true;
    services.nfs.settings = {
      nfsd.vers3 = false;
      gssd.verbosity = 2;
      gssd.rpc-verbosity = 2;
    };
    # Have to use special bind mount option for zfs
    # https://discourse.nixos.org/t/nixos-systemd-zfs-and-nfsv4-bind-mounts/58117/2
    fileSystems."/srv/nas" = {
      device = "/storage/nas";
      fsType = "none";
      options = [
        "bind"
        "nofail"
        "x-systemd.requires=zfs-mount.service"
        # "x-systemd.requires-mounts-for=/storage/nas"
      ];
    };
    services.nfs.server.exports = ''
      /srv/nas *(rw,no_root_squash,sec=krb5p)
    '';
  };
}
