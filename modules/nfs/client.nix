{lib, ...}: {
  imports = [./common.nix];
  config = {
    fileSystems = {
      "/data" = {
        device = "borg-2.msng.to:/srv/nas";
        fsType = "nfs4";
        options = [
          "nfsvers=4.2"
          "sec=krb5p"
          "noatime"
          "noauto"
        ];
      };
    };
  };
}
