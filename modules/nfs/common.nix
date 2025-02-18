{
  security.krb5 = {
    enable = true;
    settings = {
      domain_realm."msng.to" = "MSNG.TO";
      libdefaults.default_realm = "MSNG.TO";
      logging.debug = "true";
      realms."MSNG.TO" = {
        admin_server = "borg-2.msng.to";
        kdc = "borg-2.msng.to";
      };
    };
  };
  networking.domain = "msng.to";
  # specify hosts manually - otherwise a reverse IP lookup will be attempted on client connect
  networking.extraHosts = ''
    10.69.80.10 borg-0.msng.to
    10.69.80.11 borg-1.msng.to
    10.69.80.12 borg-2.msng.to
  '';
  # Disable rpcbind as it's not needed for nfsv4
  # systemd.services.rpcbind.enable = false;
}
