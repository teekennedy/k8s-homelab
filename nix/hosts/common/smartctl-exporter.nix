# SMART / NVMe health for every disk on the host, scraped by Prometheus through
# the smartctl ScrapeConfig in k8s/platform/monitoring-system.
{...}: {
  services.prometheus.exporters.smartctl = {
    enable = true;
    # SMART data changes slowly. Scrapes between queries get cached values.
    maxInterval = "5m";
    openFirewall = true;
  };
}
