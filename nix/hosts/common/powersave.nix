{pkgs, ...}: {
  # Set powersaving options
  systemd.services.power-tune = {
    description = "Power Management tunings";
    wantedBy = ["multi-user.target"];
    after = ["multi-user.target"];
    enableStrictShellChecks = true;
    script = ''
      set -euo pipefail

      ${pkgs.powertop}/bin/powertop --auto-tune

      # Disable SATA ALPM - powertop enables min_power which adds latency on wake
      for policy in /sys/class/scsi_host/host*/link_power_management_policy; do
        [ -f "$policy" ] && echo max_performance > "$policy"
      done

      # Disable NVMe APST (Autonomous Power State Transitions)
      for ctrl in /sys/class/nvme/nvme*/power/pm_qos_latency_tolerance_us; do
        [ -f "$ctrl" ] && echo 0 > "$ctrl"
      done

      # Set CPU scaling governor to powersave mode
      echo powersave | tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor

      # For intel processors that support hardware P-states, specify the energy performance preference
      # https://docs.kernel.org/admin-guide/pm/intel_epb.html
      if ls /sys/devices/system/cpu/cpufreq/policy*/energy_performance_preference 2>/dev/null; then
        echo power | tee /sys/devices/system/cpu/cpufreq/policy*/energy_performance_preference
      fi
    '';
    serviceConfig.Type = "oneshot";
  };
}
