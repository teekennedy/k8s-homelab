{
  config,
  lib,
  pkgs,
  ...
}: let
  textfileDir = "/var/lib/prometheus/node-exporter-textfiles";

  # nixos-facter lists every display controller under hardware.graphics_card.
  # Enable the exporter only where the i915 driver is bound, so this stays a
  # no-op on hosts with no Intel GPU (and on borg-3's amdgpu card, which lists
  # alongside an i915 one). The `or` defaults keep this safe on hosts whose
  # facter report lacks the attrs.
  graphicsCards = config.facter.report.hardware.graphics_card or [];
  hasIntelGpu = builtins.any (card: (card.driver or "") == "i915") graphicsCards;

  # The script lives in its own directory with a pyproject.toml so Dagger's
  # Python checks (black + pytest) discover it like the k8s-side projects.
  #
  # flake8 runs at build time via writePython3Bin with no --max-line-length, so
  # it would enforce its 79-column default while black formats at 88. Line
  # length is black's call. W503/W504 and E203 are in the ignore list because
  # `--ignore` replaces flake8's defaults rather than extending them, and black
  # actively emits all three.
  exporter = pkgs.writers.writePython3Bin "intel-gpu-textfile-exporter" {
    flakeIgnore = ["E203" "E501" "W503" "W504"];
  } (builtins.readFile ./intel-gpu-exporter/intel_gpu_exporter.py);
in {
  config = lib.mkIf hasIntelGpu {
    systemd.tmpfiles.rules = [
      "d ${textfileDir} 0755 root root -"
    ];

    systemd.services.intel-gpu-textfile-exporter = {
      description = "Export Intel GPU engine metrics for Prometheus textfile collector";
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${exporter}/bin/intel-gpu-textfile-exporter";
        Environment = [
          "INTEL_GPU_TOP_BIN=${pkgs.intel-gpu-tools}/bin/intel_gpu_top"
          "TEXTFILE_DIR=${textfileDir}"
        ];

        # intel_gpu_top reads i915 perf counters, which are privileged unless
        # perf_event_paranoid is lowered globally. CAP_PERFMON is the narrow
        # capability for that, so the unit does not need to run as full root.
        AmbientCapabilities = ["CAP_PERFMON"];
        CapabilityBoundingSet = ["CAP_PERFMON"];
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ReadWritePaths = [textfileDir];
        ProtectHome = true;
        PrivateNetwork = true;
        RestrictAddressFamilies = ["AF_UNIX"];
      };
    };

    systemd.timers.intel-gpu-textfile-exporter = {
      description = "Run the Intel GPU textfile exporter every minute";
      wantedBy = ["timers.target"];
      timerConfig = {
        OnCalendar = "*:0/1";
        Persistent = true;
      };
    };
  };
}
