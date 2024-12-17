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
  networking.useNetworkd = true;
  networking.interfaces = let
    ifaceCfg = {
      wakeOnLan.enable = false;
      useDHCP = true;
    };
  in {
    eno1 = ifaceCfg;
    eno2 = ifaceCfg;
    enp3s0 = ifaceCfg;
  };
  systemd.network.networks."99-ethernet-default-dhcp".networkConfig = {
    UseDomains = "yes";
  };

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

  # List services that you want to enable:

  # Enable systemd-resolved
  services.resolved.enable = true;
  # Enable the OpenSSH daemon.
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
    };
  };

  # Open ports in the firewall.
  networking.firewall.allowedTCPPorts = [];
  networking.firewall.allowedUDPPorts = [];

  system.stateVersion = "23.11"; # Did you read the comment?
}
