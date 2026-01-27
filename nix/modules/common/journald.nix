{...}: {
  # Store journal logs in RAM (/run/log/journal) instead of disk (/var/log/journal)
  # This reduces disk I/O which is beneficial for etcd performance.
  # Logs are ephemeral and lost on reboot, but they're collected by Loki for persistence.
  services.journald.storage = "volatile";

  # Configure retention limits to prevent excessive RAM usage
  services.journald.extraConfig = ''
    # Limit volatile storage (RAM) to 1GB
    RuntimeMaxUse=1G
    # Keep max 100MB per journal file
    RuntimeMaxFileSize=100M
    # Start removing old logs when space gets below 200MB
    RuntimeKeepFree=200M
    # Forward to syslog socket for collection by Promtail/Loki
    ForwardToSyslog=no
    # Max retention time (older logs are rotated out)
    MaxRetentionSec=1week
  '';
}
