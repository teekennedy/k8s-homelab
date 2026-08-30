# Kernel console over UDP.
#
# The journal lives in tmpfs and only persisted through Loki. When a machine
# hard-resets (a fatal MCE, a firmware reset, a hang) the final printk lines
# are lost.
#
# netconsole hands each console line to the network driver from inside printk
# itself, providing peers with all log messages that would otherwise have been
# lost on a crash. Every cluster host both sends to and receives from every
# other.
#
# Log volume is negligible: the kernel command line sets loglevel=4, so only
# KERN_ERR and worse reach the console. Machine checks, oopses and panics all
# print above that threshold; ordinary chatter does not print at all.
{
  config,
  lib,
  pkgs,
  builderClusters,
  ...
}: let
  hosts = builderClusters.borg or {};
  hostName = config.networking.hostName;
  peers = lib.filterAttrs (name: _: name != hostName) hosts;

  # Each host transmits on its own UDP port and every receiver runs one listener
  # per peer, so the socket a datagram arrives on identifies the sender. That
  # keeps the receiver a dumb pipe — no parsing, nothing to get wrong — which
  # matters because it is handling the output of a machine that is failing.
  #
  # builtins.attrNames sorts, so the assignment only has to agree between hosts
  # built from the same flake, which it does. Adding a host appends; removing
  # one renumbers the hosts after it.
  basePort = 6660;
  ports = lib.listToAttrs (lib.imap0
    (i: name: lib.nameValuePair name (basePort + i))
    (builtins.attrNames hosts));
  myPort = ports.${hostName};

  logDir = "/var/log/netconsole";

  # /sys/kernel/config/netconsole targets cannot be declared in configuration.nix:
  # they need the egress interface, source address and — because netconsole
  # builds its frames by hand and there is no ARP in panic context — the peer's
  # link address, none of which are known until the network is up.
  configureTargets = pkgs.writeShellApplication {
    name = "netconsole-configure";
    runtimeInputs = [pkgs.iproute2 pkgs.iputils pkgs.gawk];
    text = ''
      if [ ! -d /sys/kernel/config/netconsole ]; then
        echo "netconsole: configfs interface missing (module not loaded?)" >&2
        exit 1
      fi

      configure_target() {
        name="$1"
        ip="$2"
        dir="/sys/kernel/config/netconsole/$name"

        route=$(ip route get "$ip" 2>/dev/null || true)
        dev=$(printf '%s\n' "$route" | awk '{for (i=1;i<=NF;i++) if ($i=="dev") {print $(i+1); exit}}')
        src=$(printf '%s\n' "$route" | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')
        if [ -z "$dev" ] || [ -z "$src" ]; then
          echo "netconsole: no route to $name ($ip), leaving target unconfigured" >&2
          return 0
        fi

        # Prime the neighbour table so the address we copy out of it is current.
        ping -c1 -W1 "$ip" >/dev/null 2>&1 || true
        mac=$(ip neigh show "$ip" dev "$dev" | awk '{for (i=1;i<=NF;i++) if ($i=="lladdr") {print $(i+1); exit}}')
        if [ -z "$mac" ]; then
          echo "netconsole: no neighbour entry for $name ($ip), leaving target unconfigured" >&2
          return 0
        fi

        # A live target rejects attribute writes, so an existing one is either
        # already correct, or has to be torn down and rebuilt. Only the MAC
        # realistically drifts (a peer NIC swap), and a stale one sends every
        # crash report into a black hole.
        if [ -d "$dir" ]; then
          if [ "$(cat "$dir/enabled")" = "1" ] && [ "$(cat "$dir/remote_mac")" = "$mac" ]; then
            return 0
          fi
          echo 0 > "$dir/enabled"
          rmdir "$dir"
        fi

        mkdir "$dir"
        echo "$dev" > "$dir/dev_name"
        echo "$src" > "$dir/local_ip"
        echo "$ip" > "$dir/remote_ip"
        echo "$mac" > "$dir/remote_mac"
        echo ${toString myPort} > "$dir/remote_port"
        echo 1 > "$dir/enabled"
        echo "netconsole: sending $dev ($src) -> $name ($ip/$mac) port ${toString myPort}"
      }

      ${lib.concatStringsSep "\n" (lib.mapAttrsToList
        (name: host: "configure_target ${lib.escapeShellArg name} ${lib.escapeShellArg host.address}")
        peers)}
    '';
  };

  # socat writes each datagram straight through; awk stamps a wall clock onto it
  # (the kernel's own prefix is monotonic-since-boot, which is the wrong clock
  # for correlating a crash against anything else); tee keeps a copy on disk
  # under /var/log, which impermanence persists, while stdout goes to the
  # journal and on to Loki. Two copies because the interesting failures are the
  # ones where you do not get to choose which pipeline still works.
  receiverFor = name: port:
    pkgs.writeShellApplication {
      name = "netconsole-receive-${name}";
      runtimeInputs = [pkgs.socat pkgs.gawk pkgs.coreutils];
      text = ''
        socat -u "UDP4-RECV:${toString port},reuseaddr" - \
          | awk '{ print strftime("%Y-%m-%dT%H:%M:%S%z"), "${name}", $0; fflush() }' \
          | tee -a ${logDir}/${name}.log
      '';
    };
in {
  boot.kernelModules = ["netconsole"];

  systemd.tmpfiles.rules = [
    "d ${logDir} 0750 root root -"
  ];

  systemd.services =
    {
      netconsole-configure = {
        description = "Configure netconsole targets for peer hosts";
        wantedBy = ["multi-user.target"];
        wants = ["network-online.target"];
        after = ["network-online.target"];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = "${configureTargets}/bin/netconsole-configure";
        };
      };
    }
    // lib.mapAttrs' (name: _:
      lib.nameValuePair "netconsole-receive-${name}" {
        description = "Collect kernel console output from ${name}";
        wantedBy = ["multi-user.target"];
        after = ["network.target"];
        serviceConfig = {
          ExecStart = "${receiverFor name ports.${name}}/bin/netconsole-receive-${name}";
          Restart = "always";
          RestartSec = 5;
        };
      })
    peers;

  # Run on a timer to detect peers that were offline on first run.
  systemd.timers.netconsole-configure = {
    description = "Retry netconsole targets for peers that were unreachable";
    wantedBy = ["timers.target"];
    timerConfig = {
      OnBootSec = "5min";
      OnUnitActiveSec = "15min";
    };
  };

  networking.firewall.allowedUDPPorts = lib.mapAttrsToList (name: _: ports.${name}) peers;

  # tee holds the file open, so rotation has to copy and truncate rather than
  # rename out from under it.
  services.logrotate.settings."${logDir}/*.log" = {
    frequency = "weekly";
    rotate = 12;
    compress = true;
    missingok = true;
    notifempty = true;
    copytruncate = true;
  };
}
