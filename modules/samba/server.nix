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
    users = {
      smb-k8s = {
        description = "Samba mount user for k8s volume storage";
        isSystemUser = true;
        uid = 1200;
        group = config.users.groups.smb-k8s.name;
      };
    };
    groups.smb-k8s = {
      gid = 1200;
    };
  };

  # Cache the samba data dir
  environment.persistence."/cache".directories = [
    "/var/lib/samba"
  ];

  # Provision the password file for smbusers as a secret
  # Generate this file by running `sudo smbpasswd -a smb-k8s`.
  sops.secrets.smbpasswd_file = {
    mode = "0600";
    owner = config.users.users.root.name;
    group = config.users.users.root.group;
    path = "/var/lib/samba/private/passdb.tdb";
  };

  # open the ports used for netbios-less samba share
  networking.firewall.allowedTCPPorts = [445];
}
