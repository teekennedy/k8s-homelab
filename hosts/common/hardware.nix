# Settings normally found in hardware-configuration.nix that are common between hosts.
{
  config,
  pkgs,
  lib,
  ...
}: {
  networking.useNetworkd = true;
  systemd.network.networks."99-ethernet-default-dhcp".networkConfig = {
    UseDomains = "yes";
  };
  nix.settings.experimental-features = ["nix-command" "flakes"];
  # Add wheel group to nix trusted users
  nix.settings.trusted-users = ["root" "@wheel"];

  # Set your time zone.
  time.timeZone = "America/Denver";

  # Use the systemd-boot EFI boot loader.
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;
  # Limit the number of generations in the boot menu. Default is null which is unlimited.
  boot.loader.systemd-boot.configurationLimit = 120;

  # Enable systemd-resolved
  services.resolved.enable = true;
  # Enable the OpenSSH daemon.
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication = false;
      PermitRootLogin = "no";
    };
    # Don't generate host RSA key
    hostKeys = [
      {
        path = "/etc/ssh/ssh_host_ed25519_key";
        type = "ed25519";
      }
    ];
  };
}
