{pkgs, ...}: {
  # The Fitlet 3 has an issue where it will automatically reboot on shutdown when wake-on-lan is enabled.
  # Wake-on-lan is hardcoded to be enabled in BIOS, so this systemd unit disables it on every boot.
  # See https://fit-pc.com/wiki/index.php?title=Fitlet3_Errata_Notes#FITLET3ERR011:_Reboot_instead_of_shutdown_issue_when_the_LAN_port_is_connected_and_WOL_is_enabled
  systemd.services."disable-wol" = {
    description = "Disable wake-on-lan for all capable interfaces";
    wantedBy = ["multi-user.target"];
    requires = ["network.target"];
    before = ["network-online.target"];
    serviceConfig = {
      Type = "oneshot";
      ExecStart = pkgs.writeShellScript "disable-wol" ''
        for iface in /sys/class/net/*; do
          name=$(basename "$iface")
          if ${pkgs.ethtool}/bin/ethtool "$name" 2>/dev/null | grep -q "Supports Wake-on:"; then
            ${pkgs.ethtool}/bin/ethtool -s "$name" wol d
          fi
        done
      '';
    };
  };
}
