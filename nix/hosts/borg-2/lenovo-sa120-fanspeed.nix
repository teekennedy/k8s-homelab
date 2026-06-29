{inputs, ...}: {
  systemd.services.lenovo-sa120-fanspeed = {
    description = "Set Lenovo SA120 fan speed to minimum on boot";
    wantedBy = ["multi-user.target"];
    after = ["multi-user.target"];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${inputs.lenovo_sa120_fanspeed.packages.x86_64-linux.default}/bin/lenovo-sa120-fanspeed 1";
      RemainAfterExit = true;
    };
  };
}
