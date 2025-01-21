{
  inputs,
  lib,
  config,
  ...
}: {
  imports = [
    inputs.impermanence.nixosModules.impermanence
  ];

  boot.initrd.postResumeCommands = lib.mkAfter ''
    MNTPOINT=$(mktemp -d)
    mount ${config.fileSystems."/".device} $MNTPOINT
    trap 'umount $MNTPOINT; rm -rf $MNTPOINT' EXIT
    if [[ -e $MNTPOINT/root ]]; then
        mkdir -p $MNTPOINT/old_roots
        timestamp=$(date --date="@$(stat -c %Y $MNTPOINT/root)" "+%Y-%m-%-d_%H:%M:%S")
        mv $MNTPOINT/root "$MNTPOINT/old_roots/$timestamp"
    fi

    delete_subvolume_recursively() {
        IFS=$'\n'
        for i in $(btrfs subvolume list -o "$1" | cut -f 9- -d ' '); do
            delete_subvolume_recursively "$MNTPOINT/$i"
        done
        btrfs subvolume delete "$1"
    }

    for i in $(find $MNTPOINT/old_roots/ -maxdepth 1 -mtime +30); do
        delete_subvolume_recursively "$i"
    done

    btrfs subvolume create $MNTPOINT/root
  '';

  # persistent is for files/directories that are backed up
  fileSystems."/persistent".neededForBoot = true;
  # default directory perms: root:root 0755
  environment.persistence."/persistent" = {
    hideMounts = true;
    directories = [
      # Records association between user/group names and ids.
      # Without this directory, backups could have wrong uid/gid.
      "/var/lib/nixos"
    ];
    files = [
      # machine-id used by systemd
      "/etc/machine-id"
      # ssh host keys
      "/etc/ssh/ssh_host_ed25519_key"
      "/etc/ssh/ssh_host_ed25519_key.pub"
      "/etc/ssh/ssh_host_rsa_key"
      "/etc/ssh/ssh_host_rsa_key.pub"
    ];
  };

  # Cache is for files/directories that persist between boots
  # but are not backed up.
  fileSystems."/cache".neededForBoot = true;
  environment.persistence."/cache" = {
    hideMounts = true;
    directories = [
      # logs
      "/var/log"
      # core dumps
      "/var/lib/systemd/coredump"
    ];
    files = [
    ];
  };
}
