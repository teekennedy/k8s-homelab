{...}: {
  disableWakeOnLan.devices = [
    "enp0s29f1" # aka eno1
    "enp0s29f2" # aka eno2
    # "enp2s0" # This device doesn't exist as of the 2025-06-03 boot. Weird.
    "enp3s0"
  ];
}
