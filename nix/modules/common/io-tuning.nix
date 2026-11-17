{...}: {
  services.udev.extraRules = ''
    # NVMe: no kernel IO scheduler (device has internal queuing)
    ACTION=="add|change", KERNEL=="nvme[0-9]*n[0-9]*", ATTR{queue/scheduler}="none"
    # SATA SSDs: mq-deadline for fair scheduling
    ACTION=="add|change", KERNEL=="sd[a-z]*", ATTR{queue/rotational}=="0", ATTR{queue/scheduler}="mq-deadline"
    # Spinning disks: bfq for fairness under mixed workloads
    ACTION=="add|change", KERNEL=="sd[a-z]*", ATTR{queue/rotational}=="1", ATTR{queue/scheduler}="bfq"
    # Longhorn V1 volumes (iSCSI, IET VIRTUAL-DISK) claim to be rotational, so
    # they matched bfq above and the kernel gave them 8 MB of readahead. Every
    # I/O is a ~1 ms network round trip: bfq's idling made 16 parallel reads 8x
    # slower than with no scheduler, and the readahead turned a random 40 KB
    # read into 6 MB of transfer. 128 KB keeps sequential reads at ~70% of the
    # 8 MB rate (measured 2026-11-17).
    ACTION=="add|change", KERNEL=="sd[a-z]*", ATTRS{vendor}=="IET*", ATTRS{model}=="VIRTUAL-DISK*", ATTR{queue/scheduler}="none", ATTR{queue/read_ahead_kb}="128"
  '';
}
