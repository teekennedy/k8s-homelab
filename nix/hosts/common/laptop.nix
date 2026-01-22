{...}: {
  # Don't suspend / powerdown when the laptop lid is closed.
  services.logind.settings.Login.HandleLidSwitch = "ignore";

  # Turn off laptop screen to save power
  systemd.services = {
    "laptop-screen-powersave" = {
      description = "Turn off laptop screen";
      after = ["multi-user.target"];
      wantedBy = ["multi-user.target"];
      script = ''
        set -euo pipefail

        # Note: There are other, better methods for turning off the screen,
        #       but is the only method tested to work on my 2013 MBP.
        echo 0 | tee /sys/class/backlight/gmux_backlight/brightness
      '';
      serviceConfig.Type = "oneshot";
    };
  };
}
