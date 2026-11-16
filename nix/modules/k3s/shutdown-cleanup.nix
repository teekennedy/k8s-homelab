# Backstop for kubelet's graceful node shutdown: unmounts kubelet volumes it
# left behind before k3s stops, so systemd's own unmount pass can't hang on a
# Longhorn device whose engine (in the instance-manager pod) is already gone.
{pkgs, ...}: let
  cleanup = pkgs.writeShellApplication {
    name = "k3s-shutdown-cleanup";
    runtimeInputs = with pkgs; [coreutils gnugrep systemd util-linux];
    text = ''
      # ExecStop also runs when the unit is stopped by hand or removed; only
      # touch mounts during a system shutdown.
      if [ -z "$(systemctl list-jobs --no-legend shutdown.target)" ]; then
        exit 0
      fi

      # Make I/O to an unreachable Longhorn engine fail within seconds instead
      # of after the 120s iSCSI recovery timeout. Sessions already in recovery
      # keep their old timer, so their devices are offlined to fail I/O now.
      for session in /sys/class/iscsi_session/session*; do
        [ -e "$session" ] || continue
        case "$(cat "$session/targetname")" in
          iqn.2019-10.io.longhorn:*) ;;
          *) continue ;;
        esac
        echo 5 > "$session/recovery_tmo" || true
        if [ "$(cat "$session/state")" != LOGGED_IN ]; then
          echo "offlining devices of failed session $(cat "$session/targetname")"
          for state in "$session"/device/target*/*/state; do
            echo offline > "$state" || true
          done
        fi
      done

      # Deepest mounts first. /var/lib/kubelet is also bind-mounted at
      # /cache/var/lib/kubelet, so match either path. A mount still in use by
      # a leftover container is detached lazily instead.
      grep -oE '[^ ]*/var/lib/kubelet/(pods|plugins)/[^ ]*' /proc/self/mounts \
        | sort -ru \
        | while read -r target; do
          # Unmounting one copy can propagate to the other.
          mountpoint -q "$target" || continue
          if ! timeout 10 umount "$target" 2>/dev/null; then
            echo "lazily unmounting busy $target"
            umount -l "$target" || true
          fi
        done
    '';
  };
in {
  systemd.services.k3s-shutdown-cleanup = {
    description = "Unmount kubelet volumes left over after graceful node shutdown";
    wantedBy = ["multi-user.target"];
    # Stop order is reversed, so ExecStop runs before k3s stops. kubelet's
    # shutdown inhibitor has already held the shutdown until its pods stopped.
    after = ["k3s.service" "iscsid.service"];
    # Restarting would run ExecStop outside a shutdown; the guard makes that a
    # no-op, but there is no reason to restart it either.
    restartIfChanged = false;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${pkgs.coreutils}/bin/true";
      ExecStop = "${cleanup}/bin/k3s-shutdown-cleanup";
      TimeoutStopSec = "90s";
    };
  };
}
