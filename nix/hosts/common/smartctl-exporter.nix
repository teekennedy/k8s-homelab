# SMART / NVMe health for every disk on the host, scraped by Prometheus through
# the smartctl ScrapeConfig in k8s/platform/monitoring-system.
{...}: {
  nixpkgs.overlays = [
    # TODO: Remove this overlay once a smartctl_exporter release includes
    # prometheus-community/smartctl_exporter#329. In 0.15.0, devices appearing
    # or disappearing after startup (e.g. Longhorn iSCSI volumes) make /metrics
    # fail with "unregistered descriptor" until the exporter restarts.
    (_final: prev: {
      prometheus-smartctl-exporter = prev.prometheus-smartctl-exporter.overrideAttrs (_old: {
        src = prev.fetchFromGitHub {
          owner = "prometheus-community";
          repo = "smartctl_exporter";
          rev = "1ff5f688843b1e2650698354a616218421d50e9b";
          hash = "sha256-kmX/fc5zk8OeiXobzpugOkq7TejP8QHXbn9Ypvp25jo=";
        };
        vendorHash = "sha256-PJiMYWdoQ3Zk7sYHveG9uIgOjllgPuu6RjAPVdKUi6Y=";
      });
    })
  ];

  services.prometheus.exporters.smartctl = {
    enable = true;
    # SMART data changes slowly. Scrapes between queries get cached values.
    maxInterval = "5m";
    openFirewall = true;
  };
}
