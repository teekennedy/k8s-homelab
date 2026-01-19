# Interactive Changelog

Keeping track of the things I setup or change manually (outside of IaC) so I know what extra steps are involved if I ever need to recreate the cluster from scratch.

Yes, in an ideal world, this list would be empty, but not everything is worth taking the time to automate or declaratively configure.

## External access

2026-01-18

Setup port forwarding rules in Unifi to point http and https to my MetalLB IP address 10.69.80.210.
Also setup a DNS override in Unifi for external.network.msng.to, pointing to the same IP address.
This way if I try to access a public-facing k8s service, I'll still access it over the LAN.

## k3s nodes

2025-06-18

Running the k3s cluster etcd database on btrfs was really slowing things down thanks to copy-on-write.
I needed to run `chattr +C /var/lib/rancher/k3s/server/db` to disable CoW for that directory.
However that only gets applied to new files in that folder.
To work around this, one has to do a rename and copy so the files are new.
I really wanted to make this something that handles this automatically in the future, so I wrote this systemd service and configuration:

```nix
{
  config,
  lib,
  pkgs,
  ...
}: {
  options = {
    services.k3s.embeddedRegistry.enable = lib.mkOption {
      description = "Whether to enable the k3s embedded registry mirror. Configured via the registries_yaml secret in secrets.enc.yaml.  https://docs.k3s.io/installation/registry-mirror";
      default = true;
      type = lib.types.bool;
    };
    services.k3s.disableCopyOnWrite = lib.mkOption {
      description = "Whether to disable copy-on-write for the k3s server db directory. Only applicable if you're using btrfs for your k3s storage.";
      default = true;
      type = lib.types.bool;
    };
  };
  config = {
    systemd.services.k3s-db-nocow = lib.mkIf (config.services.k3s.role == "server" && config.services.k3s.disableCopyOnWrite) {
      serviceConfig.Type = "oneshot";
      description = "Disable CoW on k3s database directory (once, if btrfs)";
      wantedBy = ["k3s.service"];
      before = ["k3s.service"];
      # Only run once
      unitConfig.ConditionPathExists = "!/var/lib/rancher/k3s/.chattr-applied";
      path = with pkgs; [
        e2fsprogs # chattr, lsattr
        util-linux # findmnt
        gawk # awk
        jq # jq
      ];
      script = ''
        set -euo pipefail
        db_dir="/var/lib/rancher/k3s/server/db"

        # Create db_dir if it doesn't exist
        mkdir -p "$db_dir"

        # Only proceed if filesystem is btrfs
        if [[ "$(stat -f -c %T "$db_dir")" != "btrfs" ]]; then
          echo "Filesystem is not btrfs, skipping chattr"
          touch "$db_dir/../.chattr-applied"
          exit 0
        fi

        # Skip if all files in db_dir are already nocow
        if [[ -z "$(find "$db_dir" -type f -exec lsattr '{}' + 2>/dev/null | awk '{ print $1 }' | grep -v C)" ]]; then
          echo "All files in $db_dir are already nocow, skipping chattr"
          touch "$db_dir/../.chattr-applied"
          exit 0
        fi


        # Resolve bind mount source if any
        db_source_dir="$(findmnt --first-only --json /var/lib/rancher/k3s/server/db | jq -r '.filesystems[0].source | match("\\[(.*?)\\]") | .captures[0].string')"
        if [[ $? -eq 0 && -n "$db_source_dir" ]]; then
          db_dir="$db_source_dir"
          echo "Resolved db_dir bind mount to: $db_dir"
        fi

        echo "Would apply chattr +C to $db_dir"
        # Move, recreate with +C, and copy back contents without CoW
        mv "$db_dir" "$db_dir.bak"
        mkdir "$db_dir"
        chattr +C "$db_dir"
        cp -a --reflink=never "$db_dir.bak/." "$db_dir"
        rm -rf "$db_dir.bak"

        touch "$db_dir/../.chattr-applied"
      '';
    };
  };
}
```

Now this did exactly what I wanted, _but_ since the k3s server database dir is bind-mounted to `/persistence`, when I renamed the folder, it broke the mount, and I couldn't get it to mount properly without rebooting.
I've spent too much time on something I only need to do manually a handful of times.
I moved the config here in case I ever want to come back to it.
Maybe all it needs is an unmount before rename.

## Jellyfin downloader

- 2025-04-07: Throttled network to 10 KiB/s between 12:59 and 7:01pm every day to save energy during peak hours.

## Renovate

- 2025-03-29: Created a classic Personal Access Token in GitHub with the `repo:public_repo` scope and added it as a new secret in the global-secrets namespace called `github.renovate`.
  Updated the renovate-secret external secret definition to include this value.
