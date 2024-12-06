{
  config,
  pkgs,
  lib,
  ...
}: {
  boot.initrd.availableKernelModules = ["xhci_pci" "ahci" "usb_storage" "usbhid" "uas" "sd_mod"];
  boot.initrd.kernelModules = ["dm-snapshot"];
  boot.kernelModules = ["kvm-intel"];
  boot.extraModulePackages = [];
  networking.useDHCP = true;
  networking.useNetworkd = true;
  systemd.network.networks."99-ethernet-default-dhcp".networkConfig = {
    UseDomains = "yes";
  };

  fileSystems."/" = {
    device = "/dev/disk/by-uuid/c5de90e4-5cfb-43a1-aeed-a61c49e52881";
    fsType = "ext4";
  };

  fileSystems."/boot" = {
    device = "/dev/disk/by-uuid/CE2A-C751";
    fsType = "vfat";
  };

  fileSystems."/nix" = {
    device = "/dev/disk/by-uuid/b79a1fc6-9b6b-46ee-8d4f-f2d86e62058a";
    fsType = "ext4";
  };

  fileSystems."/home" = {
    device = "/dev/disk/by-uuid/c4bf9cdc-d3ce-4f42-b0ed-dabb5c00a767";
    fsType = "ext4";
  };

  fileSystems."/var/lib/longhorn" = {
    device = "/dev/disk/by-uuid/44cbda2d-f2ed-401b-bc93-7e2bca18ddea";
    fsType = "ext4";
  };

  swapDevices = [
    {device = "/dev/disk/by-uuid/6a6335d2-43bd-4229-9b57-a137d64e1053";}
  ];

  # copied from configuration.nix
  nix.settings.experimental-features = ["nix-command" "flakes"];

  # Use the systemd-boot EFI boot loader.
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;
  # Limit the number of generations in the boot menu. Default is null which is unlimited.
  boot.loader.systemd-boot.configurationLimit = 120;

  networking.hostName = "borg-0"; # Define your hostname.

  # Set your time zone.
  time.timeZone = "America/Denver";

  # Configure network proxy if necessary
  # networking.proxy.default = "http://user:password@proxy:port/";
  # networking.proxy.noProxy = "127.0.0.1,localhost,internal.domain";

  # Don't allow user password to be changed interactively
  users.mutableUsers = false;
  users.users.tkennedy = {
    isNormalUser = true;
    openssh.authorizedKeys.keys = [
      "sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29tAAAAIETquAokxYIU4oPwonsCbUPA09n68mQrMfJwW9q6J19IAAAACnNzaDpnaXRodWI= tkennedy@oxygen.local"
      # GPG SSH key
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPQjEqJpz5sOwxeieTNx1UBikeQ43rWnw0oQnjk+Z8z openpgp:0xEC44996F"
    ];
    extraGroups = ["wheel"]; # Enable ‘sudo’ for the user.
    packages = with pkgs; [
      file
      htop-vim
      kubectl
      tree
      vim
    ];
  };
  users.users.tkennedy.hashedPasswordFile = config.sops.secrets.tkennedy_hashed_password.path;
  # Disable sudo prompt for `wheel` users.
  security.sudo.wheelNeedsPassword = lib.mkDefault false;

  # List packages installed in system profile. To search, run:
  # $ nix search wget
  environment.systemPackages = with pkgs; [
    file
    htop-vim
    tree
    vim
  ];

  # List services that you want to enable:

  # Enable systemd-resolved
  services.resolved.enable = true;
  # Enable the OpenSSH daemon.
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "no";
      PasswordAuthentication = false;
    };
  };

  # Open ports in the firewall.
  networking.firewall.allowedTCPPorts = [];
  networking.firewall.allowedUDPPorts = [
    5353 # multicast DNS
  ];

  system.stateVersion = "23.11"; # Did you read the comment?
}
