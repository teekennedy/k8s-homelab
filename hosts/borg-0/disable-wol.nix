{
  config,
  pkgs,
  lib,
  ...
}: {
  options = {
    disableWakeOnLan.devices = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      description = "List of network devices to disable wake-on-lan.";
      default = [];
    };
  };
  config = {
    # The Fitlet 3 has an issue where it will automatically reboot on shutdown when wake-on-lan is enabled.
    # Wake-on-lan is hardcoded to be enabled in BIOS, so this systemd unit disables it on every boot.
    # See https://fit-pc.com/wiki/index.php?title=Fitlet3_Errata_Notes#FITLET3ERR011:_Reboot_instead_of_shutdown_issue_when_the_LAN_port_is_connected_and_WOL_is_enabled
    systemd.services =
      {
        "disable-wol@" = {
          description = "Disable wake-on-lan for %i";
          requires = ["network.target"];
          before = ["network-online.target"];
          serviceConfig = {
            Type = "oneshot";
            ExecStart = "${pkgs.ethtool}/bin/ethtool -s %i wol d";
            Restart = "on-failure";
            RestartSec = "5s";
          };
        };
      }
      // (lib.pipe config.disableWakeOnLan.devices [
        (map (i:
          lib.nameValuePair "disable-wol@${i}" {
            wantedBy = ["multi-user.target"];
            overrideStrategy = "asDropin";
          }))
        builtins.listToAttrs
      ]);
  };
}
