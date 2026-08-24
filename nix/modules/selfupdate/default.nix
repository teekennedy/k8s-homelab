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
  lastRevFile = "${stateDir}/last-rev";
  repoDir = "${stateDir}/repo.git";
  targetRevFile = "${runDir}/target-rev";
  runLog = "${runDir}/last-run.log";

  systemctl = "${config.systemd.package}/bin/systemctl";

  # The full argv, shared verbatim between the sudoers entries and the trigger
  # script, so the two cannot drift. sudo matches the whole argv; a mismatch
  # would fail closed (the trigger is denied) rather than open.
  selfupdateCommand = "${systemctl} start --wait nixos-selfupdate.service";
  sentinelCommand = "${systemctl} start nixos-reboot-sentinel.service";

  selfupdateScript = pkgs.writeShellApplication {
    name = "nixos-selfupdate";
    runtimeInputs = [pkgs.git pkgs.coreutils];
    runtimeEnv = {
      REPO_DIR = repoDir;
      STAMP_FILE = stampFile;
      LAST_REV_FILE = lastRevFile;
      TARGET_REV_FILE = targetRevFile;
      RUN_LOG = runLog;
      BRANCH = branch;
      ATTRIBUTE = attribute;
      FLAKE_URLS = lib.concatStringsSep " " flakeUrls;
      STALENESS_SECONDS = toString stalenessSeconds;
    };
    text = builtins.readFile ./selfupdate.sh;
  };

  sentinelScript = pkgs.writeShellApplication {
    name = "nixos-reboot-sentinel";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv.SENTINEL_FILE = sentinelFile;
    text = builtins.readFile ./sentinel.sh;
  };

  metricsScript = pkgs.writeShellApplication {
    name = "nixos-selfupdate-metrics";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv = {
      STAMP_FILE = stampFile;
      LAST_REV_FILE = lastRevFile;
      TEXTFILE_DIR = config.services.textfileCollector.directory;
    };
    text = builtins.readFile ./metrics.sh;
  };

  triggerScript = pkgs.writeShellApplication {
    name = "deploybot-trigger";
    runtimeInputs = [pkgs.coreutils];
    runtimeEnv = {
      STAMP_FILE = stampFile;
      LAST_REV_FILE = lastRevFile;
      TARGET_REV_FILE = targetRevFile;
      RUN_LOG = runLog;
      SUDO = "/run/wrappers/bin/sudo";
      SELFUPDATE_CMD = selfupdateCommand;
      SENTINEL_CMD = sentinelCommand;
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
      systemd.services.nixos-selfupdate = {
        description = "Build this host's NixOS configuration from git and stage it for next boot";
        after = ["network-online.target"];
        wants = ["network-online.target"];
        # /run/current-system/sw first so nix and nixos-rebuild come from the
        # running system rather than from a pinned nixpkgs. Determinate manages
        # nix itself, so second-guessing which nix to use here would be wrong.
        path = ["/run/current-system/sw"];
        serviceConfig = {
          Type = "oneshot";
          ExecStart = lib.getExe selfupdateScript;
          CacheDirectory = "nixos-selfupdate";
          CacheDirectoryMode = "0755";
          # A cold build of a NixOS toplevel on these hosts is minutes, not
          # hours, but a cache miss on something big should not be fatal.
          TimeoutStartSec = "2h";
        };
      };

      # Fallback only. Persistent=true needs OnCalendar (systemd.timer(5): "this
      # setting only has an effect on timers configured with OnCalendar="), which
      # is also why this is not OnUnitActiveSec=7.5d -- that measures from the
      # unit's last activation, an in-memory timestamp that resets every boot, so
      # on hosts kured reboots regularly it would sit disarmed exactly when it is
      # needed. The 7.5-day check lives in the service instead.
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

      # Unconditionally create rundir: nixos-selfupdate.service writes RUN_LOG
      # here on every run (timer fallback included), not just when the CI
      # trigger account below is enabled. Making the directory conditional on
      # userCaKeyFile means that `tee` will write to a directory that doesn't
      # exist when userCaKeyFile is null, which causes the unit to fail.
      #
      # The runDir has two files: TARGET_REV_FILE and RUN_LOG. TARGET_REV_FILE
      # is written by the deploybot user and read by nixos-selfupdate.service
      # running as root. RUN_LOG is written by nixos-selfupdate.service and
      # read by deploybot. The permissions are setup so that deploybot can
      # write TARGET_REV_FILE and read RUN_LOG, while the sticky bit prevents
      # deploybot from deleting or renaming RUN_LOG.
      systemd.tmpfiles.rules = [
        "d ${runDir} 1770 root ${user} -"
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
              command = selfupdateCommand;
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
