{
  config,
  lib,
  ...
}: {
  # Borg-3 is a 2019 MacBook Pro with screen and battery removed.
  # Its real-time clock (RTC) gets reset on every boot, even while plugged in.

  # Disable RTC trimming as the changes get lost on boot anyway.
  services.chrony.enableRTCTrimming = false;

  # k3s validates certificates at startup, so will crashloop
  # with "not yet valid" until time is synced.
  systemd.services.k3s = lib.mkIf config.services.k3s.enable {
    after = ["time-sync.target"];
    wants = ["time-sync.target"];
  };
}
