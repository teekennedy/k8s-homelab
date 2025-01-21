# nix-impermanence and btrfs-related settings as they apply to a k3s installation.
{
  lib,
  config,
  ...
}: {
  config = lib.mkIf (config.services.k3s.enable && (lib.hasAttr "persistence" config.environment)) {
    environment.persistence = {
      "/persistent" = {
        directories = [
          # k3s sqlite datastore
          "/var/lib/rancher/k3s/server/db"
        ];
      };
      "/cache" = {
        directories = [
          # containerd default metadata dir
          "/var/lib/containerd"
          # containerd default state dir
          "/run/containerd"
        ];
      };
    };
  };
}
