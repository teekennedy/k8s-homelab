# Settings normally found in hardware-configuration.nix that are common between hosts.
{
  config,
  lib,
  ...
}: let
  # nixos-facter's SMBIOS report lists a memory_device per DIMM slot, sibling to
  # memory_array. Each populated slot exposes `ecc_bits`: the number of ECC bits
  # on the module (0 = non-ECC; DDR5 ECC SODIMMs report 16). If any slot has ECC
  # bits, this host has ECC memory. `or` defaults keep this safe on hosts whose
  # facter report lacks the smbios/memory_device attrs.
  memoryDevices = config.facter.report.smbios.memory_device or [];
  hasEcc = builtins.any (dev: (dev.ecc_bits or 0) > 0) memoryDevices;
in {
  networking.useNetworkd = true;

  # On hosts with ECC memory, run rasdaemon so uncorrectable/fatal machine-check
  # errors are captured and persisted via the mce_record tracepoint (query with
  # `ras-mc-ctl --summary`). This works independently of EDAC.
  #
  # NOTE: there is currently no in-tree EDAC driver for the Arrow Lake-HX (285HX,
  # host-bridge PCI id 0x7D1C) sideband-ECC memory controller, so per-DIMM
  # correctable-error counters are unavailable — ECC still corrects in hardware,
  # we just can't observe the rate. igen6_edac is NOT the driver: it is IBECC
  # (in-band ECC) only and its PCI alias list omits 0x7D1C, so it loads but binds
  # no controller (no /sys/devices/system/edac/mc/mc0). Add the real driver via
  # boot.kernelModules here if/when one lands upstream.
  hardware.rasdaemon.enable = lib.mkIf hasEcc true;

  # sets static nameservers directly in /etc/systemd/resolved.conf.
  # This avoids having duplicate entries gathered from network devices.
  networking.nameservers = ["10.69.80.1"];

  # disable facter generated dhcp configs for networks.
  hardware.facter.detected.dhcp.enable = false;

  # turn off wifi
  systemd.network.networks."11-disable-wireless" = {
    matchConfig.Type = "wlan";
    linkConfig.Unmanaged = "yes";
  };

  # disable bluetooth
  boot.blacklistedKernelModules = ["btusb" "bluetooth"];

  # Add wheel group to nix trusted users
  nix.settings.trusted-users = ["@wheel"];

  # Set your time zone.
  time.timeZone = "America/Denver";

  # Use the systemd-boot EFI boot loader.
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;
  # Limit the number of generations in the boot menu. Default is null which is unlimited.
  boot.loader.systemd-boot.configurationLimit = 12;

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
