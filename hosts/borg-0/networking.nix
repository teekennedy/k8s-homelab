{...}: {
  systemd.network.networks."10-static-lan" = {
    matchConfig.Name = "eno1";
    networkConfig = {
      Address = "10.69.80.10/25";
      Gateway = ["10.69.80.1"];
      DNS = ["10.69.80.1"];
    };
  };
  disableWakeOnLan.devices = [
    "enp0s29f1" # aka eno1
    "enp0s29f2" # aka eno2
    "enp2s0"
    "enp3s0"
  ];
}
