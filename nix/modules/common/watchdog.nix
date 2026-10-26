# Hardware watchdog.
#
# systemd pets /dev/watchdog0 while PID 1 is alive. If the kernel or the whole
# platform freezes, the petting stops and the chipset watchdog resets the
# machine on its own, without needing the CPU or RAM to make progress.
#
# The reset is a warm reset, so machine-check registers survive it and get
# reported on the next boot. Hosts without a usable watchdog device (e.g. one
# disabled by the BIOS) log a warning and carry on.
{...}: {
  systemd.settings.Manager = {
    RuntimeWatchdogSec = "30s";
  };
}
