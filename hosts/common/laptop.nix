{pkgs, ...}: {
  # Don't suspend / powerdown when the laptop lid is closed.
  services.logind.lidSwitch = "ignore";

  systemd.services = {
    "laptop-screen-powersave" = {
      description = "Turn off laptop screen when not in use";
      after = ["multi-user.target"];
      wantedBy = ["multi-user.target"];
      serviceConfig = {
        Type = "oneshot";
        # blank laptop screen after 1 minute, powerdown after 2 minutes
        ExecStart = "${pkgs.util-linux}/bin/setterm --term linux --blank 1 --powerdown 2";
        StandardInput = "tty";
        StandardOutput = "tty";
        TTYPath = "/dev/tty1";
      };
    };
  };
}
