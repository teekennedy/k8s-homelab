# Disko configuration for raidz2 storage pool
{config, ...}: {
  # ZFS requires that networking.hostId be set
  # Generated using: head -c4 /dev/urandom | od -A none -t x4
  networking.hostId = "1f58744a";
  # enable monthly scrub
  services.zfs.autoScrub = {
    enable = true;
    interval = "monthly";
    randomizedDelaySec = "1h";
    pools = ["storage"];
  };
  # Tell zfs to leave a minimum of 10% total memory free.
  boot.extraModprobeConfig = with builtins; let
    total_mem_bytes =
      (
        head (filter
          (elem: elem.type == "phys_mem")
          (head (filter
            (elem: elem.model == "Main Memory")
            config.facter.report.hardware.memory))
              .resources)
      )
          .range;
  in ''
    options zfs zfs_arc_sys_free=${toString (floor (mul total_mem_bytes 0.1))}
  '';
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
                pool = "storage";
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
                pool = "storage";
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
                pool = "storage";
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
                pool = "storage";
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
                pool = "storage";
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
                pool = "storage";
              };
            };
          };
        };
      };
    };
    zpool = {
      storage = {
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

        datasets = {
          backup = {
            type = "zfs_fs";
          };
          nas = {
            type = "zfs_fs";
          };
        };
        # Without zfs option mountpoint = legacy,
        # both zfs and systemd try to mount the pool during startup.
        # The legacy option tells zfs not to mount automatically.
        # https://github.com/nix-community/disko/issues/581
        mountpoint = "/storage";
        rootFsOptions.mountpoint = "legacy";
        datasets.nas.mountpoint = "/storage/nas";
        datasets.nas.options.mountpoint = "legacy";
        datasets.backup.mountpoint = "/storage/backup";
        datasets.backup.options.mountpoint = "legacy";
      };
    };
  };
}
