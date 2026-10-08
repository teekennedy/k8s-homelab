# VictoriaMetrics

Long-term metrics store. Prometheus keeps 7d of raw data and remote_writes
everything here; VictoriaMetrics downsamples it and keeps it for 4y.

```
Prometheus (7d raw) --remote_write--> vmsingle
                                        -relabelConfig      drop go_*/process_*
                                        -streamAggr.config  downsample to 5m
                                        -> TSDB (retentionPeriod: 4y)
```

## Stream aggregation

[Stream aggregation](https://docs.victoriametrics.com/stream-aggregation/)
runs in vmsingle itself. Rules are in `files/stream-aggr-config.yml`:

| Metrics | Outputs |
|---|---|
| `*_total`, `*_bucket`, `*_sum`, `*_count` (counters) | `total` |
| everything else (gauges, summary quantiles) | `avg`, `min`, `max` |

Aggregation is per-series (no `by`/`without`), so only resolution changes.
Aggregated series are named `<metric>:5m_<output>`, e.g.
`node_cpu_seconds_total:5m_total`. `-streamAggr.dropInput=true` means no raw
samples are stored. Grafana's VictoriaMetrics datasource uses
`timeInterval: 5m` to match.

Verify after deploy:

```sh
VM=http://victoria-metrics-server.victoria-metrics.svc:8428
curl -s $VM/api/v1/label/__name__/values | grep -c ':5m_'      # > 0
curl -s "$VM/api/v1/query?query=count({__name__=~\"go_.*\"})"  # empty
```

## Self-monitoring

`server.serviceMonitor.enabled` has to stay on: it is what makes Prometheus
scrape vmsingle's own `vm_*` metrics, which the disk alerts in
`templates/prometheus-alert-rules.yaml` are built on. Those alerts evaluate
against Prometheus' raw 7d data, not the aggregated copy in VictoriaMetrics,
so they see full-resolution samples and unsuffixed metric names.

## Capacity

Stream aggregation only applies to data as it arrives. The ~147Gi of raw
samples ingested between 2026-02-04 and 2026-10-07 cannot be compacted after
the fact: `-downsampling.period` and per-series retention filters are
[enterprise features][enterprise], and stream aggregation keys its windows off
ingestion time rather than sample timestamps, so re-importing history through
it would stamp every sample with the time of the import.

That raw block is frozen — `-streamAggr.dropInput` means nothing is written to
those series any more — so it doesn't grow, and it ages out on its own once the
Feb 2026 partitions pass the 4y retention in 2030. Until then the volume has to
hold it on top of the aggregated data, hence the 500Gi sizing:

| Component | Size |
|---|---|
| Frozen raw history (Feb–Oct 2026) | ~147Gi |
| 4y of 5m aggregated data | ~250Gi |
| Headroom for merges (VictoriaMetrics wants 20% free) | ~100Gi |

The 250Gi estimate comes from the measured raw rate of ~0.6Gi/day, reduced by
the ~5.3x drop in sample count (30s to 5m) and offset by the gauge rules
emitting three series per input (`avg`/`min`/`max`) and by downsampled samples
compressing less well than raw ones. It assumes series churn stays roughly
flat; over four years churn is the most likely reason for an overrun, since
indexdb grows with the total number of unique series ever seen, not the active
set. Re-check it against the real numbers with:

```sh
VM=http://victoria-metrics-server.victoria-metrics.svc:8428
curl -s $VM/api/v1/status/tsdb | jq '.data.totalSeries, .data.totalLabelValuePairs'
curl -s $VM/metrics | grep -E '^(vm_data_size_bytes|vm_free_disk_space_bytes|vm_rows\{)'
```

Dropping `min`/`max` from the gauge rule in `files/stream-aggr-config.yml`
would cut the aggregated figure to roughly 130Gi, at the cost of no longer
seeing sub-5m spikes in long-term data.

[enterprise]: https://docs.victoriametrics.com/victoriametrics/enterprise/

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
  -p '{"spec":{"resources":{"requests":{"storage":"500Gi"}}}}'
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
