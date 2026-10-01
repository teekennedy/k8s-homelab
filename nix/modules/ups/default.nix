# Powers the host off after the UPS has been on battery for 60 seconds.
# See docs/ups.md.
{
  config,
  lib,
  pkgs,
  ...
}: let
  upsName = "cyberpower";
  secrets = [
    "ups_snmp_username"
    "ups_snmp_auth_password"
    "ups_snmp_priv_password"
  ];

  # The nixpkgs module writes ups.conf to the Nix store, so it's rendered to
  # /run/nut with the SNMP credentials substituted for the @name@ placeholders.
  upsConfTemplate = pkgs.writeText "ups.conf" ''
    maxstartdelay = ${toString config.power.ups.maxStartDelay}

    ${config.power.ups.ups.${upsName}.summary}
  '';

  renderUpsConf = pkgs.writeShellScript "render-ups-conf" ''
    set -euo pipefail
    umask 077
    install -m0600 -D ${upsConfTemplate} /run/nut/ups.conf
    escaped=$(mktemp)
    for name in ${lib.escapeShellArgs secrets}; do
      # NUT's parser rejects an unescaped \, ", or # even inside quotes.
      sed 's/[\\"#]/\\&/g' "$CREDENTIALS_DIRECTORY/$name" > "$escaped"
      ${pkgs.replace-secret}/bin/replace-secret "@$name@" "$escaped" /run/nut/ups.conf
    done
  '';

  upsschedCmd = pkgs.writeShellScript "upssched-cmd" ''
    if [ "$1" = onbatt ]; then
      ${pkgs.util-linux}/bin/logger -t upssched-cmd "UPS on battery for 60s, shutting down"
      exec ${config.power.ups.package}/sbin/upsmon -c fsd
    fi
  '';
in {
  config = lib.mkIf (builtins.pathExists ./secrets.enc.yaml) {
    sops.secrets = lib.genAttrs (secrets ++ ["upsmon_password"]) (_: {
      sopsFile = ./secrets.enc.yaml;
      mode = "0400";
      restartUnits = ["nut-ups-conf.service" "upsd.service" "upsdrv.service" "upsmon.service"];
    });

    power.ups = {
      enable = true;
      mode = "standalone";

      ups.${upsName} = {
        driver = "snmp-ups";
        # Addressed by IP so polling doesn't depend on DNS during an outage.
        port = "10.69.110.15";
        description = "CyberPower CP1500PFCRM2U (RMCARD205)";
        # Never cut UPS output from a host; others may still be shutting down.
        shutdownOrder = -1;
        directives = [
          "mibs = cyberpower"
          "snmp_version = v3"
          "secLevel = authPriv"
          ''secName = "@ups_snmp_username@"''
          "authProtocol = SHA"
          ''authPassword = "@ups_snmp_auth_password@"''
          "privProtocol = AES"
          ''privPassword = "@ups_snmp_priv_password@"''
          # Every host polls the card, so poll less often than the 2s default.
          "pollinterval = 5"
        ];
      };

      users.upsmon = {
        passwordFile = config.sops.secrets.upsmon_password.path;
        upsmon = "primary";
      };

      upsmon.monitor.${upsName} = {
        system = "${upsName}@localhost";
        user = "upsmon";
        type = "primary";
      };

      # The default SHUTDOWNCMD (`shutdown now`) goes through logind, so
      # kubelet's graceful node shutdown inhibitor still gets to drain pods.
      upsmon.settings = {
        # Skips the killpower step, which would turn off the UPS outlets.
        POWERDOWNFLAG = null;
        # EXEC passes these to upssched (the default NOTIFYCMD).
        NOTIFYFLAG = [
          ["ONBATT" "SYSLOG+WALL+EXEC"]
          ["ONLINE" "SYSLOG+WALL+EXEC"]
        ];
      };

      schedulerRules = toString (pkgs.writeText "upssched.conf" ''
        CMDSCRIPT ${upsschedCmd}
        PIPEFN /run/nut/upssched/upssched.pipe
        LOCKFN /run/nut/upssched/upssched.lock

        AT ONBATT * START-TIMER onbatt 60
        AT ONLINE * CANCEL-TIMER onbatt
      '');
    };

    environment.etc."nut/ups.conf".source = lib.mkForce "/run/nut/ups.conf";

    systemd.tmpfiles.rules = [
      "d /run/nut/upssched 0750 ${config.power.ups.upsmon.user} ${config.power.ups.upsmon.group} -"
    ];

    systemd.services.nut-ups-conf = {
      description = "Render NUT ups.conf with SNMP credentials";
      before = ["upsd.service" "upsdrv.service"];
      requiredBy = ["upsd.service" "upsdrv.service"];
      restartTriggers = [upsConfTemplate];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = renderUpsConf;
        LoadCredential = map (name: "${name}:${config.sops.secrets.${name}.path}") secrets;
        PrivateTmp = true;
      };
    };

    # Hosts can boot before the network or UPS card is reachable, e.g. after
    # an outage, so retry rather than leave the host unmonitored.
    systemd.services.upsdrv = {
      wants = ["network-online.target"];
      after = ["network-online.target"];
      restartTriggers = [upsConfTemplate];
      startLimitIntervalSec = 0;
      serviceConfig = {
        Restart = "on-failure";
        RestartSec = "30s";
      };
    };
  };
}
