{...}: {
  services.udev.extraRules = ''
    # NVMe: no kernel IO scheduler (device has internal queuing)
    ACTION=="add|change", KERNEL=="nvme[0-9]*n[0-9]*", ATTR{queue/scheduler}="none"
    # SATA SSDs: mq-deadline for fair scheduling
    ACTION=="add|change", KERNEL=="sd[a-z]*", ATTR{queue/rotational}=="0", ATTR{queue/scheduler}="mq-deadline"
    # Spinning disks: bfq for fairness under mixed workloads
    ACTION=="add|change", KERNEL=="sd[a-z]*", ATTR{queue/rotational}=="1", ATTR{queue/scheduler}="bfq"
  '';
}
