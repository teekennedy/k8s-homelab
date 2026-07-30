{pkgs, ...}: let
  textfileDir = "/var/lib/prometheus/node-exporter-textfiles";

  # The script lives in its own directory with a pyproject.toml so that Dagger's
  # Python checks (black + pytest) discover it like the k8s-side projects.
  #
  # writePython3Bin validates the script with flake8 at build time, and passes
  # no --max-line-length, so flake8 enforces its 79-column default while black
  # formats this repo at its own default of 88. Line length is black's call, so
  # flake8 must not also have an opinion or the formatter and the build fight.
  #
  # This is the standard black-compatibility ignore set, not just E501: the
  # writer builds `--ignore`, which *replaces* flake8's default ignore list
  # rather than extending it, so W503/W504 (line break around binary operator)
  # would otherwise become active — and those are things black actively emits.
  # E203 (whitespace before ':') is black's slice style and was never defaulted.
  exporter = pkgs.writers.writePython3Bin "zfs-textfile-exporter" {
    flakeIgnore = ["E203" "E501" "W503" "W504"];
  } (builtins.readFile ./zfs-exporter/zfs_exporter.py);
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
