{
  config,
  inputs,
  lib,
  ...
}: {
  # Disko module that creates btrfs subvolumes for use with nix impermanence
  # Usage: Get a stable identifier for the root disk using `lsblk` to find the device name
  # and then `udevadm info <disk> | grep disk/by-id` to get the disk's stable identifier.
  # Set disko.devices.disk.main.device equal to this identifier.
  imports = [
    inputs.disko.nixosModules.disko
  ];
  options = {
    disko.swapFileSize = lib.mkOption {
      description = "Size of the swapfile. Must include one of K,M,G,T,P as unit. Defaults to half of the available memory.";
      example = "200M";
      type = lib.types.strMatching "[0-9]+[KMGTP]?";
      default = let
        halfMemMb =
          (
            builtins.head (builtins.filter
              (elem: elem.type == "phys_mem")
              (builtins.head (builtins.filter
                (elem: elem.model == "Main Memory")
                config.facter.report.hardware.memory))
              .resources)
          )
          .range
          / 2
          / 1024
          / 1024;
      in
        (builtins.toString halfMemMb) + "M";
    };
    disko.longhornDevice = lib.mkOption {
      description = "Optional device to use for the longhorn volume. Use `udevadm info <disk> | grep disk/by-id` to get the device's stable identifier.";
      example = "/dev/disk/by-id/xxxxx";
      default = "";
      type = lib.types.str;
    };
  };
  config = {
    disko.devices = {
      disk =
        {
          main = {
            type = "disk";
            content = {
              type = "gpt";
              partitions = {
                ESP = {
                  priority = 1;
                  name = "ESP";
                  start = "1M";
                  end = "128M";
                  type = "EF00";
                  content = {
                    type = "filesystem";
                    format = "vfat";
                    mountpoint = "/boot";
                    mountOptions = ["umask=0077"];
                  };
                };
                root = {
                  size = "100%";
                  content = {
                    type = "btrfs";
                    extraArgs = ["-f"]; # Override existing partition
                    # Subvolumes must set a mountpoint in order to be mounted,
                    # unless their parent is mounted
                    subvolumes = {
                      # Subvolume name is different from mountpoint
                      "/root" = {
                        mountpoint = "/";
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                      };
                      "/home" = {
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                        mountpoint = "/home";
                      };
                      "/nix" = {
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                        mountpoint = "/nix";
                      };
                      # Persistent is for data I want to persist and backup
                      "/persistent" = {
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                        mountpoint = "/persistent";
                      };
                      # Cache is for data I want to persist between reboots but not backup
                      "/cache" = {
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                        mountpoint = "/cache";
                      };

                      # Subvolume for the swapfile
                      "/swap" = {
                        mountpoint = "/.swapvol";
                        swap = {
                          swapfile.size = config.disko.swapFileSize;
                        };
                      };
                    };
                    mountpoint = "/partition-root";
                    swap = {
                      swapfile = {
                        size = config.disko.swapFileSize;
                      };
                    };
                    # Create a snapshot of the empty root subvolume
                    postCreateHook = ''
                      MNTPOINT=$(mktemp -d)
                      mount "/dev/disk/by-partlabel/disk-main-root" "$MNTPOINT" -o subvol=/
                      trap 'umount $MNTPOINT; rm -rf $MNTPOINT' EXIT
                      btrfs subvolume snapshot -r $MNTPOINT/root $MNTPOINT/root-blank
                    '';
                  };
                };
              };
            };
          };
        }
        // lib.optionalAttrs (builtins.stringLength config.disko.longhornDevice > 0) {
          longhorn = {
            type = "disk";
            device = config.disko.longhornDevice;
            content = {
              type = "gpt";
              partitions = {
                longhorn = {
                  size = "100%";
                  content = {
                    type = "filesystem";
                    format = "ext4";
                    mountpoint = "/var/lib/longhorn";
                  };
                };
              };
            };
          };
        };
    };
  };
}
