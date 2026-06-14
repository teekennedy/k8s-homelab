# NFS with Mutual TLS

NFS volumes in the cluster use kernel-native mutual TLS (`xprtsec=mtls`, RFC 9289). This provides:

- **Encryption**: all NFS traffic between cluster nodes and the NFS server is encrypted via TLS
- **Mutual authentication**: both the NFS server and each NFS client node present certificates signed by the internal CA; connections without a valid cert are rejected at the export level
- **Certificate lifecycle management**: certificates are issued and automatically renewed by cert-manager; no manual credential steps per node

---

## How it works

```
┌─────────────────────────────┐        ┌─────────────────────────────────┐
│  k3s node (client)          │        │  NFS server                     │
│                             │        │                                 │
│  kernel NFS client          │◄──────►│  nfsd + tlshd                   │
│  │                          │        │  │                              │
│  ▼                          │        │  │ cert: /var/lib/nfs-mtls/     │
│  tlshd (client mode)        │        │  ▼                              │
│  │                          │        │  /storage/nas/k8s               │
│  │ cert: /var/lib/nfs-mtls/ │        │  (xprtsec=mtls required)        │
└─────────────────────────────┘        └─────────────────────────────────┘
        ▲                                          ▲
        │       cert issuance (cert-manager)       │
        └──────────────────────────────────────────┘
                          │
              ┌───────────▼──────────┐
              │  cert-manager        │
              │  internal-ca issuer  │
              │  cert-system ns      │
              └──────────────────────┘
                          │
              ┌───────────▼──────────┐
              │  nfs-mtls-cert-sync  │
              │  DaemonSet           │
              │  (writes to host fs) │
              └──────────────────────┘
```

### Certificate issuance (cert-manager)

cert-manager issues one `Certificate` resource per borg node (`nfs-mtls-cert-borg-N`) via the `internal-ca` ClusterIssuer. The CA is a self-signed ECDSA root cert stored in Secret `internal-ca-cert` in `cert-system`. cert-manager renews host certs automatically 30 days before their 90-day expiry.

The trust-manager `Bundle` named `cluster-ca-bundle` distributes the internal CA cert to a ConfigMap of the same name in every namespace, making it available to workloads that need to verify internal TLS (e.g. Authelia verifying the LDAP cert).

### Getting certs onto the host (cert-sync DaemonSet)

The `nfs-mtls-cert-sync` DaemonSet (in `cert-system`) runs one pod per node:

1. Each pod mounts all 4 node cert Secrets as volumes (`optional: true`).
2. It uses `spec.nodeName` (via Downward API) to select the correct cert directory (`/certs/<nodename>/`).
3. Every 60 seconds it compares the mounted cert files to `/var/lib/nfs-mtls/` on the host. If changed, it copies the files and reloads `tlshd` on the host via `nsenter`.
4. Kubelet auto-updates the mounted Secret volumes within ~60s of cert-manager renewal, so cert rotation propagates to the host within ~2 minutes with no manual action.

### TLS handshake (NFS mount time)

When a pod mounts an NFS volume with `xprtsec=mtls`:

1. The kernel NFS client contacts `tlshd` (a userspace daemon) via a Unix socket
2. `tlshd` performs the TLS handshake with the NFS server using `/var/lib/nfs-mtls/cert.pem` and `key.pem`
3. The server's `tlshd` validates the client cert against `/var/lib/nfs-mtls/ca.crt`
4. The client's `tlshd` validates the server cert against the same CA cert
5. If both certs are valid, the TLS session is established and the NFS connection proceeds

The NFS export on borg-2 specifies `xprtsec=mtls`, meaning the server **rejects** connections without a valid client cert from the internal CA.

---

## Setup

Setup is fully automated — no manual credential steps. The cert-system ArgoCD application handles everything on the Kubernetes side. You only need to deploy the NixOS changes.

### 1. Sync cert-system

Push changes and let ArgoCD sync the `cert-system` application. Verify:

```sh
# Internal CA and host certs are issued
kubectl get certificate -n cert-system
# All should show READY=True

# DaemonSet is running on all nodes
kubectl get pods -n cert-system -l app.kubernetes.io/name=nfs-mtls-cert-sync -o wide

# Certs have been synced to hosts (check one node)
ssh borg-0 ls -la /var/lib/nfs-mtls/
```

### 2. Deploy NixOS changes

Deploy borg-2 first — it's the NFS server and must be ready before clients try mTLS mounts:

```sh
lab host deploy borg-2 --boot
ssh borg-2 sudo touch /var/run/reboot-required
# Wait for kured to orchestrate the borg-2 reboot, then:
lab host deploy borg-0 --boot && ssh borg-0 sudo touch /var/run/reboot-required
lab host deploy borg-1 --boot && ssh borg-1 sudo touch /var/run/reboot-required
lab host deploy borg-3 --boot && ssh borg-3 sudo touch /var/run/reboot-required
```

`--boot` activates the profile on next boot. `touch /var/run/reboot-required` signals kured to schedule the reboot with proper cordoning.

### 3. Verify

```sh
# NFS export shows xprtsec=mtls on borg-2
ssh borg-2 exportfs -v

# tlshd running on all nodes
for h in borg-0 borg-1 borg-2 borg-3; do
  echo "=== $h ==="; ssh $h systemctl status tlshd.service --no-pager -l
done

# Test PVC provisions and binds
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nfs-mtls-test
  namespace: default
spec:
  accessModes: [ReadWriteMany]
  storageClassName: nfs-backups
  resources:
    requests:
      storage: 1Gi
EOF
kubectl wait --for=condition=Bound pvc/nfs-mtls-test -n default --timeout=60s
# Clean up (nfs-backups uses Retain — also delete the PV manually)
kubectl delete pvc -n default nfs-mtls-test
```

---

## Creating a PVC with the NFS storage class

The `nfs` StorageClass mounts from borg-2 with `xprtsec=mtls`. For RWX volumes backed by the NAS:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-app-nas
  namespace: my-app
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: nfs
  resources:
    requests:
      storage: 100Gi
```

The CSI provisioner creates `/storage/nas/k8s/<pv-uuid>/` on borg-2 automatically. If the PVC is deleted and the StorageClass has `reclaimPolicy: Delete`, the directory is also deleted.

For retained backup storage (directory kept on PVC deletion):

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-app-backups
  namespace: my-app
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: nfs-backups
  resources:
    requests:
      storage: 10Gi
```

In an `app-template` chart, mount it alongside a RWO Longhorn volume:

```yaml
# values.yaml
app-template:
  controllers:
    main:
      pod:
        securityContext:
          runAsUser: 1200
          runAsGroup: 1200
          fsGroup: 1200
          fsGroupChangePolicy: OnRootMismatch
  persistence:
    data:
      accessMode: ReadWriteOnce
      type: persistentVolumeClaim
      size: 10Gi
      storageClass: longhorn
    nas-data:
      accessMode: ReadWriteMany
      type: persistentVolumeClaim
      size: 1Ti
      storageClass: nfs
      advancedMounts:
        main:
          my-container:
            - path: /nas
```

### UID/GID ownership

NFS passes through numeric UIDs/GIDs from the server. Files on `/storage/nas/k8s` should be owned by the UID/GID used by your workloads. Set consistent `runAsUser`/`runAsGroup`/`fsGroup` in the pod security context and ensure directories on the NFS server have matching ownership:

```sh
chown -R 1200:1200 /storage/nas/k8s/<directory>
```

---

## Module reference (`nix/modules/nfs-mtls`)

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `services.nfs-mtls.enable` | bool | false | Enable the module |
| `services.nfs-mtls.serverMode` | bool | false | Export `/storage/nas/k8s` with xprtsec=mtls, run tlshd as server |
| `services.nfs-mtls.clientMode` | bool | false | Run tlshd as client (required on all k3s nodes that mount NFS) |

**Both `serverMode` and `clientMode` can be enabled simultaneously** — you can have a host that is both the NFS server and a k3s node that may schedule NFS-backed pods.

Cert paths are fixed at `/var/lib/nfs-mtls/` and written by the `nfs-mtls-cert-sync` DaemonSet. No per-host configuration is needed beyond enabling the appropriate modes.

---

## Rotating the internal CA

If the root CA needs to be replaced (compromise, algorithm migration, etc.):

### 1. Generate a new CA via cert-manager

Delete the existing `internal-ca` Certificate and `internal-ca-cert` Secret in `cert-system`. cert-manager will re-issue a new self-signed CA cert on the next reconcile (triggered by ArgoCD sync).

For a zero-downtime rotation, create a new Certificate with a different `secretName` first, update the ClusterIssuer to use it, then remove the old one.

### 2. Update the trust-manager Bundle

The `cluster-ca-bundle` Bundle in `cert-system/templates/cluster-ca-bundle.yaml` already points to `internal-ca-cert`. Once the Secret is updated by cert-manager, trust-manager automatically propagates the new CA cert to all namespace ConfigMaps within ~60 seconds.

### 3. Force cert-sync reload

During CA rotation, existing host certs are still valid until their 90-day expiry. New certs signed by the new CA will be issued by cert-manager and synced to hosts by the DaemonSet automatically. To force immediate rotation:

```sh
# Delete host cert Secrets to force cert-manager to re-issue
for node in borg-0 borg-1 borg-2 borg-3; do
  kubectl delete secret -n cert-system nfs-mtls-cert-${node}
done
# cert-manager will re-issue within ~60s; DaemonSet will sync within ~60s after that
```

---

## Troubleshooting

**Cert not synced to host after deploy**

Check the DaemonSet pod on the affected node:
```sh
kubectl logs -n cert-system -l app.kubernetes.io/name=nfs-mtls-cert-sync \
  --field-selector spec.nodeName=<node> --tail=30
```

Common causes:
- cert-manager hasn't issued the cert yet — check `kubectl get certificate -n cert-system`
- The `internal-ca` ClusterIssuer isn't ready — check `kubectl get clusterissuer internal-ca`

**tlshd not starting**

```sh
systemctl status tlshd.service
journalctl -u tlshd.service -n 50
```

Most common cause: cert files don't exist at `/var/lib/nfs-mtls/` yet. Wait for the DaemonSet to sync (check its logs above), then `systemctl start tlshd.service`.

**NFS mount fails with "connection refused" or "permission denied"**

```sh
# Check tlshd is running on the client node
systemctl status tlshd.service

# Verify xprtsec=mtls export is active on the nfs server
ssh <nfs-server> exportfs -v

# Check TLS kernel module is loaded
lsmod | grep ^tls
```

**PVC stuck in Pending**

```sh
kubectl describe pvc <pvc-name> -n <namespace>
kubectl logs -n csi-driver-nfs -l app=csi-nfs-controller
```

If the error is TLS-related, ensure the CSI controller pod's node has the cert synced and tlshd running.
