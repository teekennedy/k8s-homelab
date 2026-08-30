{...}: {
  # Store journal logs on disk (/var/log/journal).
  # /var/log is persisted by nix-impermanence.
  services.journald.storage = "persistent";

  services.journald.extraConfig = ''
    # On-disk limits. Journal volume was measured at ~3MB/day/host as of 2026-08-30.
    SystemMaxUse=1G
    SystemMaxFileSize=100M
    # The default is 15% of the filesystem, which on a 1TB disk disk is a ~150G
    # reservation that would silently stop logging if it were filled that far.
    SystemKeepFree=2G

    # journald still writes to /run/log/journal early in boot and flushes to
    # disk once /var is mounted, so these still bound that window.
    RuntimeMaxUse=1G
    RuntimeMaxFileSize=100M
    RuntimeKeepFree=200M

    # Collection is via promtail reading the journal directly, not syslog.
    ForwardToSyslog=no
    # Max retention time (older logs are rotated out)
    MaxRetentionSec=1week
  '';
}
