# Pull-based NixOS continuous deployment. See docs/nixos-cd.md for the design
# and the rationale; this file is the host half of it.
#
# Until ./deploybot_user_ca.pub exists the remote-trigger half stays inert and
# the units can only be driven by hand or by the fallback timer. Create it once
# per cluster with scripts/setup-ssh-ca.sh.
{
  config,
  lib,
  pkgs,
  ...
}: let
  cfg = config.services.nixos-selfupdate;

  # Only `enable` is an option: there is exactly one consumer (flake.nix) and it
  # varies none of these. They are named here so the scripts and the sshd config
  # agree, not to be configured.
  user = "deploybot";
  branch = "main";
  attribute = config.networking.hostName;

  # Tried in order. The forge runs on this very cluster, so the public mirror is
  # what lets a host still update itself while the cluster is down.
  flakeUrls = [
    "https://git.msng.to/ops/k8s-homelab.git"
    "https://github.com/teekennedy/k8s-homelab.git"
  ];

  sentinelFile = "/run/reboot-required";

  # How long since the last success before the fallback timer does anything.
  # Sized just past the weekly cadence of Renovate's flake-update PRs, so it
  # only acts in a week where the CI trigger was missed entirely.
  stalenessSeconds = builtins.floor (7.5 * 24 * 60 * 60); # 7.5 days

  # OpenSSH user CA public key sshd trusts for the trigger account. When absent
  # the account and its sshd rules are not created at all. Certificates are
  # minted in-cluster and rotate every few hours; this public half is the only
  # static piece and never rotates.
  userCaKeyFile =
    if builtins.pathExists ./deploybot_user_ca.pub
    then ./deploybot_user_ca.pub
    else null;

  stateDir = "/var/cache/nixos-selfupdate";
  runDir = "/run/${user}";

  stampFile = "${stateDir}/last-success";
  # Rev of the generation this host last *staged* (promoted to
  # /nix/var/nix/profiles/system), not merely built. Both the build step's
  # backwards guard and the stage step's match check read this.
  lastRevFile = "${stateDir}/last-rev";
  # Rev this host most recently built successfully, and a GC root pointing at
  # that build's toplevel. Staging never rebuilds: it just promotes whatever
  # these two agree on.
  builtRevFile = "${stateDir}/built-rev";
  builtSystemLink = "${stateDir}/built-system";
  repoDir = "${stateDir}/repo.git";
  # Consumed on read, same pattern for both stages: the caller writes the rev
  # it wants acted on, then starts the corresponding unit.
  buildTargetFile = "${runDir}/build-target-rev";
  stageTargetFile = "${runDir}/stage-target-rev";
  buildLog = "${runDir}/last-build.log";
  stageLog = "${runDir}/last-stage.log";
  # Nix's internal-json event stream for the build in progress, and the arrival
  # timestamps the follower records alongside it. Both are in the run dir
  # rather than the state dir because they describe one run and are replaced at
  # the start of the next; nothing reads them after the exporter has.
  #
  # /run being tmpfs is the point, not an accident. Nix flushes one line per
  # event, and while paths are being substituted that runs at a few hundred
  # KB/s -- roughly 1.6% of the store bytes fetched -- onto a disk the build is
  # already writing the store to. Keeping it in RAM costs nothing here: the log
  # is truncated at the start of every run, and 1.6% of even a very large
  # rollout is far short of /run's default 25%-of-RAM ceiling.
  nixLogJson = "${runDir}/nix-log.json";
  nixTimings = "${runDir}/nix-timings.txt";

  # The build metrics themselves live in the state dir, which is under
  # /var/cache and so survives the reboot every rollout ends with. The copy in
  # the textfile collector directory does not, hence the tmpfiles rule below.
  buildMetricsFile = "${stateDir}/nixos_selfupdate_build.prom";
  textfileDir = config.services.textfileCollector.directory;

  systemctl = "${config.systemd.package}/bin/systemctl";

  # The full argv, shared verbatim between the sudoers entries and the trigger
  # script, so the two cannot drift. sudo matches the whole argv; a mismatch
  # would fail closed (the trigger is denied) rather than open.
  buildCommand = "${systemctl} start --wait nixos-selfupdate-build.service";
  stageCommand = "${systemctl} start --wait nixos-selfupdate-stage.service";
  sentinelCommand = "${systemctl} start nixos-reboot-sentinel.service";

  buildScript = pkgs.writeShellApplication {
    name = "nixos-selfupdate-build";
    # gnugrep for `grep --line-buffered`, which the script's output filter uses;
    # busybox grep has no such flag and the unit's PATH is not guaranteed to put
    # GNU grep first.
    runtimeInputs = [pkgs.git pkgs.coreutils pkgs.gnugrep];
    runtimeEnv = {
      REPO_DIR = repoDir;
      LAST_REV_FILE = lastRevFile;
      BUILT_REV_FILE = builtRevFile;
      BUILT_SYSTEM_LINK = builtSystemLink;
      TARGET_REV_FILE = buildTargetFile;
      RUN_LOG = buildLog;
      BRANCH = branch;
      ATTRIBUTE = attribute;
      FLAKE_URLS = lib.concatStringsSep " " flakeUrls;
      NIX_LOG_JSON = nixLogJson;
      NIX_TIMINGS = nixTimings;
      BUILD_METRICS_FILE = buildMetricsFile;
      TEXTFILE_DIR = textfileDir;
      METRICS_CMD = lib.getExe buildMetricsScript;
    };
    text = builtins.readFile ./build.sh;
  };

  stageScript = pkgs.writeShellApplication {
    name = "nixos-selfupdate-stage";
    # nix-env comes from the unit's `path` (the running system's nix), same
    # reasoning as nixos-selfupdate-build.service above.
    runtimeInputs = [pkgs.git pkgs.coreutils];
    runtimeEnv = {
      REPO_DIR = repoDir;
      BUILT_REV_FILE = builtRevFile;
      BUILT_SYSTEM_LINK = builtSystemLink;
      LAST_REV_FILE = lastRevFile;
      STAMP_FILE = stampFile;
      TARGET_REV_FILE = stageTargetFile;
      RUN_LOG = stageLog;
    };
    text = builtins.readFile ./stage.sh;
  };

  # The fallback timer's only caller: nothing gates it on the rest of the
  # fleet, so it can safely chain build straight into stage on this host alone.
  fallbackScript = pkgs.writeShellApplication {
    name = "nixos-selfupdate-fallback";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv = {
      STAMP_FILE = stampFile;
      BUILT_REV_FILE = builtRevFile;
      STAGE_TARGET_FILE = stageTargetFile;
      STALENESS_SECONDS = toString stalenessSeconds;
      SYSTEMCTL = systemctl;
    };
    text = builtins.readFile ./fallback.sh;
  };

  sentinelScript = pkgs.writeShellApplication {
    name = "nixos-reboot-sentinel";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv.SENTINEL_FILE = sentinelFile;
    text = builtins.readFile ./sentinel.sh;
  };

  # Reads the JSON event stream the build leaves at nixLogJson and writes the
  # "what did this run actually do" half of the metrics: how many derivations
  # this host built itself, and how many paths it pulled from cache.nixos.org
  # versus from a peer over the LAN.
  #
  # Its own directory with a pyproject.toml so it is discovered like every
  # other Python project in the repo. flakeIgnore is the standard
  # black-compatibility set: writePython3Bin builds flake8's
  # --ignore, which replaces the default ignore list rather than extending it,
  # so W503/W504 would otherwise become active on things black actively emits.
  # See ../../hosts/borg-2/zfs-textfile-exporter.nix, which explains this at
  # length.
  buildMetricsScript = pkgs.writers.writePython3Bin "nixos-selfupdate-build-metrics" {
    flakeIgnore = ["E203" "E501" "W503" "W504"];
  } (builtins.readFile ./build-metrics/nix_build_metrics.py);

  metricsScript = pkgs.writeShellApplication {
    name = "nixos-selfupdate-metrics";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv = {
      STAMP_FILE = stampFile;
      LAST_REV_FILE = lastRevFile;
      TEXTFILE_DIR = textfileDir;
    };
    text = builtins.readFile ./metrics.sh;
  };

  triggerScript = pkgs.writeShellApplication {
    name = "deploybot-trigger";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv = {
      BUILT_REV_FILE = builtRevFile;
      LAST_REV_FILE = lastRevFile;
      STAMP_FILE = stampFile;
      BUILD_TARGET_FILE = buildTargetFile;
      BUILD_LOG = buildLog;
      STAGE_TARGET_FILE = stageTargetFile;
      STAGE_LOG = stageLog;
      SUDO = "/run/wrappers/bin/sudo";
      BUILD_CMD = buildCommand;
      STAGE_CMD = stageCommand;
      SENTINEL_CMD = sentinelCommand;
      # How many times the trigger may re-start a unit when it turns out to
      # have joined an update that was already running. Three covers the
      # realistic case -- a couple of pushes landing while a slow build runs --
      # without letting a host that is perpetually busy hang the pipeline.
      MAX_ATTEMPTS = "3";
    };
    text = builtins.readFile ./trigger.sh;
  };

  # Every directive after a Match line belongs to that block, so this has to be
  # the tail of sshd_config. NixOS emits services.openssh.settings first and
  # extraConfig after it, and mkAfter puts this behind any extraConfig another
  # module contributes. The assertion below makes that fail at build time rather
  # than silently applying these restrictions to everyone.
  matchBlock = ''
    Match User ${user}
      AuthorizedKeysFile none
      ForceCommand ${lib.getExe triggerScript}
      PermitTTY no
      AllowTcpForwarding no
      AllowAgentForwarding no
      X11Forwarding no
      PermitTunnel no
  '';
in {
  options.services.nixos-selfupdate.enable =
    lib.mkEnableOption "pull-based NixOS self-update driven from CI";

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      # Builds this host's NixOS configuration from git and leaves the result as
      # a GC-rooted symlink; it does not touch the system profile. Separate from
      # staging (see docs/nixos-cd.md for why) so staging is never held up
      # rebuilding something that already succeeded.
      systemd.services.nixos-selfupdate-build = {
        description = "Build this host's NixOS configuration from git";
        after = ["network-online.target"];
        wants = ["network-online.target"];
        # /run/current-system/sw first so nix and nixos-rebuild come from the
        # running system rather than from a pinned nixpkgs. Determinate manages
        # nix itself, so second-guessing which nix to use here would be wrong.
        path = ["/run/current-system/sw"];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe buildScript;
          CacheDirectory = "nixos-selfupdate";
          CacheDirectoryMode = "0755";
          # A cold build of a NixOS toplevel on these hosts is minutes, not
          # hours, but a cache miss on something big should not be fatal.
          TimeoutStartSec = "2h";
        };
      };

      # Promotes a build this host already made to the next-boot profile. Never
      # builds anything itself, so it is fast and safe to run as soon as this
      # host's own build has succeeded.
      systemd.services.nixos-selfupdate-stage = {
        description = "Stage this host's most recent NixOS build for next boot";
        path = ["/run/current-system/sw"];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe stageScript;
          CacheDirectory = "nixos-selfupdate";
          CacheDirectoryMode = "0755";
          # Activation runs the new generation's activation scripts, which can
          # do real work (users, systemd units), but nothing here builds.
          TimeoutStartSec = "10m";
        };
      };

      # Fallback only, driven by the timer below. Chains build straight into
      # stage on this one host; there is no fleet to gate against when nothing
      # triggered CI in over a week.
      systemd.services.nixos-selfupdate = {
        description = "Fallback NixOS self-update when CI has not run one recently";
        path = ["/run/current-system/sw"];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe fallbackScript;
          # `start --wait` on both nixos-selfupdate-build (2h) and
          # nixos-selfupdate-stage (10m) blocks inside this unit, so its own
          # timeout has to cover both in full or systemd can kill it mid-stage
          # after a build that ran close to its own cap.
          TimeoutStartSec = "2h15min";
        };
      };

      # Persistent=true needs OnCalendar (systemd.timer(5): "this setting only
      # has an effect on timers configured with OnCalendar="), which is also
      # why this is not OnUnitActiveSec=7.5d -- that measures from the unit's
      # last activation, an in-memory timestamp that resets every boot, so on
      # hosts kured reboots regularly it would sit disarmed exactly when it is
      # needed. The 7.5-day check lives in fallback.sh instead.
      systemd.timers.nixos-selfupdate = {
        description = "Fallback NixOS self-update when CI has not run one recently";
        wantedBy = ["timers.target"];
        timerConfig = {
          OnCalendar = "daily";
          Persistent = true;
          RandomizedDelaySec = "1h";
        };
      };

      systemd.services.nixos-reboot-sentinel = {
        description = "Create the kured reboot sentinel when a new generation is staged";
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe sentinelScript;
        };
      };

      systemd.services.nixos-selfupdate-metrics = {
        description = "Export NixOS self-update state for the Prometheus textfile collector";
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe metricsScript;
        };
      };

      systemd.timers.nixos-selfupdate-metrics = {
        description = "Refresh NixOS self-update metrics";
        wantedBy = ["timers.target"];
        timerConfig = {
          OnBootSec = "2min";
          OnUnitActiveSec = "5min";
        };
      };

      # Group is created unconditionally (and used to own rundir below) even
      # though only the CI trigger account below ever joins it: an unused
      # group is inert, but the group name has to resolve at boot regardless
      # of whether the CA key -- and thus the deploybot user -- exists yet.
      users.groups.${user} = {};

      # Unconditionally create rundir: the build and stage units write their
      # logs here on every run (timer fallback included), not just when the CI
      # trigger account below is enabled. Making the directory conditional on
      # userCaKeyFile means that `tee` will write to a directory that doesn't
      # exist when userCaKeyFile is null, which causes the unit to fail.
      #
      # The runDir holds four files: the two *_TARGET_FILEs, written by the
      # deploybot user and read by the corresponding unit running as root, and
      # the two *_LOGs, written by those units and read by deploybot. The
      # permissions are set up so that deploybot can write the target files and
      # read the logs, while the sticky bit prevents deploybot from deleting or
      # renaming a log out from under the unit still writing it.
      systemd.tmpfiles.rules = [
        "d ${runDir} 1770 root ${user} -"
        # Put the last build's metrics back in the textfile collector directory
        # after the root subvolume is rolled back on boot. `C` only copies when
        # the destination is missing, so a fresher file written later in the
        # boot is never overwritten. Deliberately scoped to this one file: the
        # timer-driven writers in that directory rewrite themselves every few
        # minutes, and restoring their last output would let a broken writer
        # hide behind a stale reading.
        "C ${textfileDir}/nixos_selfupdate_build.prom 0644 root root - ${buildMetricsFile}"
      ];
      # Cache Nix's fetcher, tarball and eval caches for the root user, which
      # is the user who runs the build and stage units.
      environment.persistence."/cache".directories = [
        {
          directory = "/root/.cache/nix";
          mode = "0700";
        }
      ];
    }

    # Remote trigger. Off until the cluster-side SSH CA exists.
    (lib.mkIf (userCaKeyFile != null) {
      users.users.${user} = {
        isSystemUser = true;
        group = user;
        # ForceCommand is executed through the account's login shell, so this
        # cannot be nologin. The shell is never reachable interactively: the
        # Match block forces the trigger script and denies a pty.
        shell = pkgs.bashInteractive;
        home = "/var/empty";
        description = "CI trigger account for NixOS self-update";
        # Generates /etc/ssh/authorized_principals.d/${user} and sets
        # AuthorizedPrincipalsFile for us. This is the principal sshd matches
        # the certificate against; there is no authorized_keys file at all.
        openssh.authorizedPrincipals = [user];
      };

      security.sudo.extraRules = [
        {
          users = [user];
          commands = [
            {
              command = buildCommand;
              options = ["NOPASSWD"];
            }
            {
              command = stageCommand;
              options = ["NOPASSWD"];
            }
            {
              command = sentinelCommand;
              options = ["NOPASSWD"];
            }
          ];
        }
      ];

      # Only read when a client actually offers a certificate, so this costs
      # ordinary publickey logins nothing.
      services.openssh.settings.TrustedUserCAKeys = "${userCaKeyFile}";
      services.openssh.extraConfig = lib.mkAfter matchBlock;

      assertions = [
        {
          assertion = lib.hasSuffix matchBlock config.services.openssh.extraConfig;
          message = ''
            The nixos-selfupdate Match block is no longer last in sshd_config.
            Something else appended to services.openssh.extraConfig after it,
            which would apply the deploybot restrictions (ForceCommand, no pty)
            to whatever follows.
          '';
        }
      ];
    })
  ]);
}
