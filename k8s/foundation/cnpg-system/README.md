# CloudNative-PG (CNPG)

CloudNative-PG operator for managing PostgreSQL clusters as Kubernetes-native resources. Used by crowdsec, gitea, and authelia.

## Runbooks

### Migrate a CNPG Cluster to a Different Storage Class

PVC `storageClass` is immutable in Kubernetes — CNPG cannot migrate a live cluster to a new storage class in place. The PVCs must be recreated, which means the data must be dumped and restored. Downtime is required.

**Before you start**: identify the cluster you're migrating. The steps below use shell variables throughout.

```bash
NAMESPACE=<namespace>          # e.g. gitea
CLUSTER=<cluster-name>         # e.g. gitea-psql
DB=<database-name>             # e.g. gitea
ARGOCD_APP=<argocd-app-name>   # e.g. gitea
```

Find the current primary pod:

```bash
kubectl get cluster $CLUSTER -n $NAMESPACE -o jsonpath='{.status.currentPrimary}'
```

#### 1. Dump the database

Scale down the application that writes to postgres first to ensure a clean dump. For example:

```bash
kubectl scale deployment/<app> -n $NAMESPACE --replicas=0
```

Then dump from the primary:

```bash
PRIMARY=$(kubectl get cluster $CLUSTER -n $NAMESPACE -o jsonpath='{.status.currentPrimary}')
kubectl exec -n $NAMESPACE $PRIMARY -- pg_dump -U postgres $DB > ${DB}_backup.sql
```

Verify the dump file is non-empty before continuing.

#### 2. Pause ArgoCD sync

```bash
argocd app set $ARGOCD_APP --sync-policy none
```

Or pause via the ArgoCD UI. This prevents ArgoCD from fighting you while resources are being deleted.

#### 3. Delete the CNPG cluster and its PVCs

```bash
kubectl delete cluster $CLUSTER -n $NAMESPACE
```

Wait for the cluster pods to terminate, then delete the PVCs:

```bash
kubectl get pvc -n $NAMESPACE | grep $CLUSTER
kubectl delete pvc -n $NAMESPACE ${CLUSTER}-1 ${CLUSTER}-2
# Add more if the cluster had more than 2 instances
```

> The CNPG Cluster deletion does NOT automatically delete PVCs — this is intentional data protection. You must delete them manually.

#### 4. Update the chart and sync

Ensure the storage class change is committed and pushed, then re-enable ArgoCD sync:

```bash
argocd app set $ARGOCD_APP --sync-policy automated
argocd app sync $ARGOCD_APP
```

Wait for the new cluster to reach `Cluster in healthy state`:

```bash
kubectl get cluster $CLUSTER -n $NAMESPACE -w
```

#### 5. Restore the database

Find the new primary:

```bash
PRIMARY=$(kubectl get cluster $CLUSTER -n $NAMESPACE -o jsonpath='{.status.currentPrimary}')
```

Restore:

```bash
kubectl exec -i -n $NAMESPACE $PRIMARY -- psql -U postgres $DB < ${DB}_backup.sql
```

Scale the application back up:

```bash
kubectl scale deployment/<app> -n $NAMESPACE --replicas=<original>
```

#### 6. Verify

Check that the application is healthy and the data is intact. Then confirm the PVCs are now on the correct storage class:

```bash
kubectl get pvc -n $NAMESPACE | grep $CLUSTER
```

---

### Recover from "Not Enough Disk Space"

CNPG will halt the cluster and report `Not enough disk space` if a postgres pod runs out of disk. The fix is to expand the PVCs.

CNPG supports live PVC expansion (`resizeInUseVolumes: true` is set in all cluster specs here). To trigger a resize, update the `storage.size` in the cluster's chart template and push — ArgoCD will apply the change, which patches the CNPG Cluster spec, which in turn resizes the PVCs automatically.

To check which instance is out of space, look at the CNPG operator logs:

```bash
kubectl logs -n cnpg-system deploy/cnpg-cloudnative-pg | grep -i "disk space"
```

The log line will name the specific instance pod (e.g. `instanceNames: ["crowdsec-psql-4"]`).

After the resize, monitor recovery:

```bash
kubectl get cluster <cluster> -n <namespace> -w
```

If a replica was left in a timeline mismatch state (FATAL: `requested timeline X is not a child of this server's history`), CNPG will automatically rebuild it from the primary once the primary is healthy.
