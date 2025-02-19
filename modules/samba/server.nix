# Creates a samba server
{config, ...}: {
  services.samba = {
    enable = true;
    # disable netbios service. only needed to access share using hostname on windows
    nmbd.enable = false;
    settings = {
      global = {
        # SMB3 protocol supported by Windows 8 and above
        "server min protocol" = "SMB3";
        "server smb encrypt" = "required";
        "server string" = "Homelab samba share on %h";
        # disable printer sharing
        # https://wiki.archlinux.org/title/Samba#Disable_printer_sharing
        "load printers" = "no";
        printing = "bsd";
        "printcap name" = "/dev/null";
        "disable spoolss" = "yes";
        "show add printer wizard" = "no";
      };
      k8s = {
        comment = "Volume storage for kubernetes cluster";
        path = "/storage/nas/k8s";
        writable = "yes";
        # public = no is the default, but it's good to be explicit
        public = "no";
        "valid users" = "@smb-k8s";
      };
    };
  };
  # Create samba users and groups
  users = {
    users =
      {
        smb-k8s = {
          description = "Samba mount user for k8s volume storage";
          isSystemUser = true;
          uid = 1200;
          group = config.users.groups.smb-k8s.name;
        };
      }
      // (builtins.listToAttrs (builtins.map (i: {
          name = "smb-k8s-${toString i}";
          value = {
            description = "Samba user ${toString i} for k8s volume storage";
            isSystemUser = true;
            uid = 1208 + i;
            group = config.users.groups.smb-k8s.name;
          };
        })
        [0 1 2 3 4 5 6 7]));
    groups.smb-k8s = {
      gid = 1208;
    };
  };
  # open the ports used for netbios-less samba share
  networking.firewall.allowedTCPPorts = [445];
}
