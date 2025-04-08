# Settings normally found in hardware-configuration.nix that are common between hosts.
{...}: {
  networking.useNetworkd = true;
  # sets static nameservers directly in /etc/systemd/resolved.conf.
  # This avoids having duplicate entries gathered from network devices.
  networking.nameservers = ["10.69.80.1"];
  systemd.network.networks."10-ethernet-static" = {
    matchConfig = {
      Type = "ether";
      Kind = "!*"; # exclude all "special" network devices, e.g. tunnel, bridge, virtual.
    };
  };
  # turn off wifi
  systemd.network.networks."11-disable-wireless" = {
    matchConfig.Type = "wlan";
    linkConfig.Unmanaged = "yes";
  };
  # disable bluetooth
  boot.blacklistedKernelModules = ["btusb" "bluetooth"];
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
