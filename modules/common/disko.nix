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
      description = "Size of the swapfile. Must include one of K,M,G,T,P as unit.";
      example = "200M";
      type = lib.types.strMatching "[0-9]+[KMGTP]?";
      default = "8G";
    };
    disko.longhornDevice = lib.mkOption {
      description = "Device to use for the longhorn volume. Use `udevadm info <disk> | grep disk/by-id` to get the device's stable identifier.";
      example = "/dev/disk/by-id/xxxxx";
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
                      # Subvolume name is the same as the mountpoint
                      "/home" = {
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                        mountpoint = "/home";
                      };
                      # Sub(sub)volume doesn't need a mountpoint as its parent is mounted
                      "/home/user" = {};
                      # Parent is not mounted so the mountpoint must be set
                      "/nix" = {
                        mountOptions = ["compress=zstd" "noatime" "discard=async"];
                        mountpoint = "/nix";
                      };
                      # Subvolume for the swapfile
                      "/swap" = {
                        mountpoint = "/.swapvol";
                        swap = {
                          swapfile.size = config.disko.swapFileSize;
                        };
                      };
                    };
                    # Create a snapshot of the empty root subvolume
                    postCreateHook = "btrfs subvolume snapshot -r /mnt/root /mnt/root-blank";

                    mountpoint = "/partition-root";
                    swap = {
                      swapfile = {
                        size = config.disko.swapFileSize;
                      };
                    };
                  };
                };
              };
            };
          };
        }
        // lib.optionalAttrs (builtins.hasAttr "longhornDevice" config.disko) {
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
