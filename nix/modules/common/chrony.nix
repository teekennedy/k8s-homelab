{
  config,
  lib,
  builderClusters,
  ...
}: let
  # Every other borg host, each polled as a server and allowed as a client.
  peers = lib.mapAttrsToList (_: host: host.address) (
    lib.filterAttrs (name: _: name != config.networking.hostName) (builderClusters.borg or {})
  );
in {
  # Every host uses the same upstream servers
  # so their errors remain relatively in sync.
  # None of these smear leap seconds, so they can be mixed.
  networking.timeServers = [
    "time.cloudflare.com"
    "time-a-b.nist.gov"
    "time-b-b.nist.gov"
    "time-a-wwv.nist.gov"
    "time-b-wwv.nist.gov"
  ];

  services.chrony = {
    enable = true;
    # At startup, set the clock to the drift file's mtime if that's later than
    # both the clock and the RTC. Covers hosts whose RTC has no battery.
    extraFlags = ["-s"];
    extraConfig = ''
      ${lib.concatMapStrings (peer: ''
          server ${peer} iburst
          allow ${peer}
        '')
        peers}
      # Orphan mode: with no upstream reachable, the host with the lowest IP
      # serves its own clock at stratum 10 and the rest follow it. A host that
      # isn't synchronised (e.g. just booted) is never used as a source.
      local stratum 10 orphan
    '';
  };

  # Holds time-sync.target until the clock is synced, like chrony's upstream
  # chrony-wait.service. After the timeout the unit fails and boot carries on
  # (e.g. no network), since nothing Requires it.
  systemd.services.chrony-wait = {
    description = "Wait for chrony to synchronise the system clock";
    after = ["chronyd.service"];
    requires = ["chronyd.service"];
    before = ["time-sync.target"];
    wants = ["time-sync.target"];
    wantedBy = ["multi-user.target"];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      # Wait until the clock has been updated and less than 0.1s of
      # correction remains, checking every second.
      ExecStart = "${config.services.chrony.package}/bin/chronyc -h 127.0.0.1,::1 waitsync 0 0.1 0.0 1";
      TimeoutStartSec = "3min";
      DynamicUser = true;
    };
  };

  # chrony's allow lines restrict which hosts get answers.
  networking.firewall.allowedUDPPorts = [123];

  # The drift file's mtime is what -s restores the clock from.
  environment.persistence."/cache".directories = [
    {
      directory = config.services.chrony.directory;
      user = "chrony";
      group = "chrony";
      mode = "0750";
    }
  ];
}
