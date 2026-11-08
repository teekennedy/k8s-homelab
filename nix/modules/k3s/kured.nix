# kured runs this script, in the host mount namespace, to reboot the node.
# It lives under /run and tmpfiles never overwrites it, so editing it changes
# kured's next reboot (e.g. to `systemctl poweroff`) until the node boots again.
{...}: {
  systemd.tmpfiles.rules = [
    "f /run/reboot-command 0755 root root - #!/bin/sh\\nexec /run/current-system/sw/bin/systemctl reboot"
  ];
}
