{...}: {
  disableWakeOnLan.devices = [
    "enp0s29f1" # aka eno1
    "enp0s29f2" # aka eno2
    # enp2s0 didn't exist as of the 2025-06-03 boot. Weird.
    # Checked again on 2026-02-15 and it's back.
    "enp2s0"
    "enp3s0"
  ];
  systemd.network = {
    netdevs = {
      "bond0" = {
        netdevConfig = {
          Kind = "bond";
          Name = "bond0";
        };
        bondConfig = {
          Mode = "802.3ad";
          MIIMonitorSec = "1s";
          LACPTransmitRate = "fast";
          UpDelaySec = "2s";
          DownDelaySec = "8s";
          TransmitHashPolicy = "layer3+4";
        };
      };
    };
    networks = {
      "10-static-ip" = {
        matchConfig = {
          Name = "enp2s0 enp3s0 bond0";
        };
        networkConfig = {
          Address = "10.69.80.10/25";
          Gateway = ["10.69.80.1"];
        };
      };
      "30-eno1" = {
        matchConfig.Name = "eno1";
        networkConfig.Bond = "bond0";
      };
      "30-eno2" = {
        matchConfig.Name = "eno2";
        networkConfig.Bond = "bond0";
      };
      "40-bond0" = {
        matchConfig.Name = "bond0";
        linkConfig = {
          RequiredForOnline = "carrier";
        };
        networkConfig.LinkLocalAddressing = "no";
      };
    };
  };
}
