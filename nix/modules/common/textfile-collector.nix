# Single owner of the node-exporter textfile collector directory.
#
# node-exporter reads this path via a hostPath mount; see
# prometheus-node-exporter.extraVolumes in
# k8s/platform/monitoring-system/values.yaml. That copy of the path cannot be
# shared across the nix/helm boundary, but everything on the nix side should
# come from here rather than restating the literal.
{
  config,
  lib,
  ...
}: {
  options.services.textfileCollector.directory = lib.mkOption {
    type = lib.types.str;
    default = "/var/lib/prometheus/node-exporter-textfiles";
    description = ''
      Directory node-exporter's textfile collector reads .prom files from.
      Writers drop a file here; this module owns creating the directory, so
      that adding a second writer does not mean a second tmpfiles rule
      asserting the same invariant.
    '';
  };

  config.systemd.tmpfiles.rules = [
    "d ${config.services.textfileCollector.directory} 0755 root root -"
  ];
}
