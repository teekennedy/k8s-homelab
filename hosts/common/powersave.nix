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
