# Disko configuration for raidz2 pool
{
  # ZFS requires that networking.hostId be set
  # Generated using: head -c4 /dev/urandom | od -A none -t x4
  networking.hostId = "1f58744a";
  disko.devices = {
    disk = {
      nas-a = {
        type = "disk";
        device = "/dev/disk/by-id/scsi-35000039ce8623969";
        content = {
          type = "gpt";
          partitions = {
            zfs = {
              size = "100%";
              content = {
                type = "zfs";
                pool = "tank";
              };
            };
          };
        };
      };
      nas-b = {
        type = "disk";
        device = "/dev/disk/by-id/scsi-35000039ce83379a5";
        content = {
          type = "gpt";
          partitions = {
            zfs = {
              size = "100%";
              content = {
                type = "zfs";
                pool = "tank";
              };
            };
          };
        };
      };
      nas-c = {
        type = "disk";
        device = "/dev/disk/by-id/scsi-35000039ce8623ac1";
        content = {
          type = "gpt";
          partitions = {
            zfs = {
              size = "100%";
              content = {
                type = "zfs";
                pool = "tank";
              };
            };
          };
        };
      };
      nas-d = {
        type = "disk";
        device = "/dev/disk/by-id/scsi-35000039ce8636fd9";
        content = {
          type = "gpt";
          partitions = {
            zfs = {
              size = "100%";
              content = {
                type = "zfs";
                pool = "tank";
              };
            };
          };
        };
      };
      nas-e = {
        type = "disk";
        device = "/dev/disk/by-id/scsi-35000039ce8623f99";
        content = {
          type = "gpt";
          partitions = {
            zfs = {
              size = "100%";
              content = {
                type = "zfs";
                pool = "tank";
              };
            };
          };
        };
      };
      nas-f = {
        type = "disk";
        device = "/dev/disk/by-id/scsi-35000039ce8623f91";
        content = {
          type = "gpt";
          partitions = {
            zfs = {
              size = "100%";
              content = {
                type = "zfs";
                pool = "tank";
              };
            };
          };
        };
      };
    };
    zpool = {
      tank = {
        type = "zpool";
        mode = "raidz2";
        rootFsOptions = {
          # enable zstd compression
          compression = "zstd";
          # Eliminate IOs used to update access times
          atime = "off";
          # Allow for per-user permissions
          acltype = "posixacl";
          # Don't store extended attributes in hidden folders.
          xattr = "sa";
        };

        mountpoint = "/data";

        datasets = {
          backup = {
            type = "zfs_fs";
            mountpoint = "/data/backup";
          };
          nas = {
            type = "zfs_fs";
            mountpoint = "/data/nas";
          };
        };
        # Without mountpoint = legacy,
        # both zfs and systemd try to mount the pool during startup.
        # The legacy option tells zfs not to mount automatically.
        # https://github.com/nix-community/disko/issues/581
        rootFsOptions.mountpoint = "legacy";
        datasets.nas.options.mountpoint = "legacy";
        datasets.backup.options.mountpoint = "legacy";
      };
    };
  };
}
