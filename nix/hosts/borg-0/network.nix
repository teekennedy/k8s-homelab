{...}: {
  systemd.network.networks."10-ethernet-static" = {
    matchConfig = {
      Type = "ether";
      Kind = "!*"; # exclude all "special" network devices, e.g. tunnel, bridge, virtual.
    };
    networkConfig = {
      Address = "10.69.80.10/25";
      Gateway = ["10.69.80.1"];
    };
  };
}
