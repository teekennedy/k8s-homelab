{...}: {
  # My nvme controller keeps failing and getting disabled by the controller.
  # dmesg output suggests using these kernel parameters to turn off APST
  # (Autonomous Power State Transition)
  boot.kernelParams = [
    "nvme_core.default_ps_max_latency_us=0"
    "pcie_aspm=off"
    "pcie_port_pm=off"
  ];
}
