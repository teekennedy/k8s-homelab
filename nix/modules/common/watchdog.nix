# Hardware watchdog and kernel lockup detectors.
#
# systemd pets /dev/watchdog0 while PID 1 is alive. If the kernel or the whole
# platform freezes, the petting stops and the chipset watchdog resets the
# machine on its own, without needing the CPU or RAM to make progress.
#
# The reset is a warm reset, so machine-check registers survive it and get
# reported on the next boot. Hosts without a usable watchdog device (e.g. one
# disabled by the BIOS) log a warning and carry on.
#
# A partial freeze defeats the hardware watchdog: with most CPUs stuck in soft
# lockups, PID 1 still runs on the rest and keeps petting it. The kernel's own
# detectors cover that case by panicking, and kernel.panic reboots 10s later.
# The hard lockup detector also prints the backtrace of a CPU frozen with
# interrupts disabled, which the soft lockup detector cannot.
{...}: {
  systemd.settings.Manager = {
    RuntimeWatchdogSec = "30s";
  };

  boot.kernel.sysctl = {
    "kernel.softlockup_panic" = 1;
    "kernel.nmi_watchdog" = 1;
    "kernel.hardlockup_panic" = 1;
    "kernel.panic" = 10;
  };
}
