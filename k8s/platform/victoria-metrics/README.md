# VictoriaMetrics

Long-term metrics store. Prometheus keeps 7d of raw data and remote_writes
everything here; VictoriaMetrics downsamples it and keeps it for 4y.

```
Prometheus (7d raw) --remote_write--> vmsingle
                                        -relabelConfig      keep allowlist
                                        -streamAggr.config  downsample to 5m
                                        -> TSDB (retentionPeriod: 4y)
```

## Stream aggregation

[Stream aggregation](https://docs.victoriametrics.com/stream-aggregation/)
runs in vmsingle itself. Rules are in `files/stream-aggr-config.yml`:

| Metrics | Outputs |
|---|---|
| `*_total`, `*_bucket`, `*_sum`, `*_count` (counters) | `total` |
| everything else (gauges, summary quantiles) | `avg` |

Aggregation is per-series (no `by`/`without`), so only resolution changes.
One output per input series: `min`/`max` on the gauge rule would store three
series per gauge, which the volume doesn't have room for (see "Capacity").
Every rule sets `keep_metric_names`, so aggregated series keep the input's
name and the same PromQL works against Prometheus and VictoriaMetrics.
`-streamAggr.dropInput=true` means no raw samples are stored. Grafana's
VictoriaMetrics datasource uses `timeInterval: 5m` to match.

## Allowlist

`server.relabel.config` in `values.yaml` keeps only metrics worth having for
years (capacity and usage trends, availability, hardware wear, alert history)
and drops everything else before stream aggregation, so the rest only lives in
Prometheus' 7d window. Container usage is stored through the
`namespace_container:*` recording rules in
`k8s/platform/monitoring-system/templates/prometheus-rule-longterm.yaml`
rather than the raw cAdvisor series: those carry a `pod` label that changes on
every rollout, and each new pod name is a new series in the 4y index.

To keep a new metric long-term, add it to the allowlist. History starts from
when the change lands; nothing is backfilled.

## Config reloads

vmsingle re-reads both `-relabelConfig` and `-streamAggr.config` on
`/-/reload` (or SIGHUP), but has no file-watch interval. The `config-reloader`
sidecar watches both mounted ConfigMaps and calls `/-/reload` when either
changes, so an Argo CD sync applies config changes without a restart. A config
with errors is rejected and the previous one stays active; check the
`victoria-metrics-server` container log after a change.

Verify after deploy:

```sh
VM=http://victoria-metrics-server.victoria-metrics.svc:8428
curl -s "$VM/api/v1/query?query=count(up)"                     # > 0
curl -s "$VM/api/v1/query?query=count({__name__=~\"go_.*\"})"  # empty
```

## Self-monitoring

`server.serviceMonitor.enabled` has to stay on: it is what makes Prometheus
scrape vmsingle's own `vm_*` metrics, which the disk alerts in
`templates/prometheus-alert-rules.yaml` are built on. Those alerts evaluate
against Prometheus' raw 7d data, not the aggregated copy in VictoriaMetrics,
so they see full-resolution samples and unsuffixed metric names.

## Capacity

Sizes for the full 4y retention, from rates measured on this instance's 5m
aggregated data (~0.63 bytes per sample, ~160 bytes of indexdb per series per
day):

| Component | 4y size |
|---|---|
| Samples: ~2.5k series x 288/day x 1461 days, about 1.05 billion | ~0.7Gi |
| indexdb: per-day index for every series, plus churn (alerts, mounts) | ~0.7Gi |
| Total | ~1.5Gi |

The 10Gi volume leaves room for the allowlist to grow about fivefold, for
background merges, and for vmbackup snapshots. Each additional 1k allowlisted
series adds roughly 0.6Gi over 4y. Series churn matters more than the active
count, since indexdb grows with every unique series ever seen; that is why the
relabel config strips pod-specific labels before aggregation.

Check the real numbers with:

```sh
VM=http://victoria-metrics-server.victoria-metrics.svc:8428
curl -s $VM/api/v1/status/tsdb | jq '.data.totalSeries, .data.totalLabelValuePairs'
curl -s $VM/metrics | grep -E '^(vm_data_size_bytes|vm_free_disk_space_bytes|vm_rows\{)'
```

The daily growth of `sum(vm_data_size_bytes)` is the simplest measure of
whether the estimate holds.

## Resizing the volume

`persistentVolume.size` renders into the StatefulSet's `volumeClaimTemplates`,
which is immutable and only applies to newly created PVCs, so growing the
volume is a two-part operation: expand the live PVC by hand, then recreate the
StatefulSet so Argo CD stops seeing a diff it can't apply. Argo CD already
ignores `/spec/resources/requests/storage` on PVCs (see `application.yaml`),
so the expanded PVC won't be reverted.

Expand the PVC **before** merging a `size` increase that also raises
`-storage.minFreeDiskSpaceBytes`. The flag takes effect as soon as the pod
restarts, and if the volume has less free space than the limit, vmsingle goes
read-only and stops storing metrics.

```sh
kubectl -n victoria-metrics patch pvc server-volume-victoria-metrics-server-0 \
  -p '{"spec":{"resources":{"requests":{"storage":"<new size>"}}}}'
# Longhorn 1.12 expands ext4 online; watch it land.
kubectl -n victoria-metrics get pvc server-volume-victoria-metrics-server-0 -w

kubectl -n victoria-metrics delete sts victoria-metrics-server --cascade=orphan
argocd app sync victoria-metrics
```

### Reclaiming space instead

If the volume can't be grown, the only reliable way to free space is to let
retention drop whole monthly partitions — set `retentionPeriod` to e.g. `90d`,
wait for the oldest partitions to disappear, then set it back to `4y`. Data
removed this way does not come back when retention is raised again.

`/api/v1/admin/tsdb/delete_series` is not a substitute: it only marks series
deleted in the index, and space is reclaimed during background merges, which
[never run for partitions that no longer receive writes][single-node]. Forcing
them with `/internal/force_merge` needs free disk space to merge into, which is
exactly what's missing when the volume is full.

[single-node]: https://docs.victoriametrics.com/victoriametrics/single-server-victoriametrics/

## Backups

`templates/backup-cronjob.yaml` runs `vmbackup` daily at 03:15. It mounts the
server's Longhorn PVC read-only (pinned to the same node via `podAffinity`),
takes a snapshot through `/snapshot/create`, and writes it incrementally to
`/storage/nas/backups/victoriametrics/latest` on borg-2 over mTLS NFS.
History comes from the `nas-backups-weekly` restic job
(`nix/hosts/borg-2/nas-backups.nix`), which ships that directory offsite.

Since vmbackup is the backup path, the server PVC is in Longhorn's
`ephemeral` recurring-job group (trim only) rather than `backup`.

Run a backup by hand:

```sh
kubectl -n victoria-metrics create job --from=cronjob/victoria-metrics-backup vm-backup-manual
kubectl -n victoria-metrics logs job/vm-backup-manual -f
```

### Restore

1. To restore an older version, first restore it from restic into
   `/storage/nas/backups/victoriametrics` on borg-2.
2. Stop the server:
   `kubectl -n victoria-metrics scale sts victoria-metrics-server --replicas=0`
3. Run `vmrestore` against the server PVC:
   ```sh
   kubectl -n victoria-metrics run vmrestore --rm -i --restart=Never \
     --image=victoriametrics/vmrestore:v1.153.0 --overrides='{"spec":{
       "securityContext":{"runAsUser":65534,"runAsGroup":65534,"fsGroup":65534},
       "containers":[{"name":"vmrestore","image":"victoriametrics/vmrestore:v1.153.0",
         "args":["-storageDataPath=/storage","-src=fs:///backup/latest"],
         "volumeMounts":[{"name":"data","mountPath":"/storage"},
                         {"name":"backup","mountPath":"/backup","readOnly":true}]}],
       "volumes":[
         {"name":"data","persistentVolumeClaim":{"claimName":"server-volume-victoria-metrics-server-0"}},
         {"name":"backup","persistentVolumeClaim":{"claimName":"victoria-metrics-backups"}}]}}'
   ```
4. Start the server again with `--replicas=1`.

## Upgrading PVC labels

PVC labels are set through `server.persistentVolume.extraLabels`, which lands
in `volumeClaimTemplates` and so has the same immutability problem as
`size` (see "Resizing the volume"). Orphan-delete the StatefulSet (pods and
PVC stay), relabel the existing PVC, then let Argo CD recreate it:

```sh
kubectl -n victoria-metrics delete sts victoria-metrics-server --cascade=orphan
kubectl -n victoria-metrics label pvc server-volume-victoria-metrics-server-0 \
  recurring-job.longhorn.io/source=enabled \
  recurring-job-group.longhorn.io/ephemeral=enabled
argocd app sync victoria-metrics
```
