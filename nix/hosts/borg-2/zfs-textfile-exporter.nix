{pkgs, ...}: let
  textfileDir = "/var/lib/prometheus/node-exporter-textfiles";

  exporter = pkgs.writers.writePython3Bin "zfs-textfile-exporter" {} (builtins.readFile ./zfs_exporter.py);
in {
  systemd.tmpfiles.rules = [
    "d ${textfileDir} 0755 root root -"
  ];

  systemd.services.zfs-textfile-exporter = {
    description = "Export ZFS pool metrics for Prometheus textfile collector";
    serviceConfig = {
      Type = "oneshot";
      ExecStart = "${exporter}/bin/zfs-textfile-exporter";
      Environment = [
        "ZPOOL_BIN=${pkgs.zfs}/bin/zpool"
        "TEXTFILE_DIR=${textfileDir}"
      ];
    };
  };

  systemd.timers.zfs-textfile-exporter = {
    description = "Run ZFS textfile exporter every minute";
    wantedBy = ["timers.target"];
    timerConfig = {
      OnCalendar = "*:0/1";
      Persistent = true;
    };
  };
}
