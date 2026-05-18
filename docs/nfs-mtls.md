# NFS with Mutual TLS

NFS volumes in the cluster use kernel-native mutual TLS (`xprtsec=mtls`, RFC 9289). This provides:

- **Encryption**: all NFS traffic between cluster nodes and the NFS server is encrypted via TLS
- **Mutual authentication**: both the NFS server and each NFS client node present certificates signed by the internal CA; connections without a valid cert are rejected at the export level
- **Certificate lifecycle management**: certificates are issued and automatically renewed by step-ca (the internal CA) via ACME

---

## How it works

```
┌─────────────────────────────┐        ┌─────────────────────────────────┐
│  k3s node (client)          │        │  NFS server                     │
│                             │        │                                 │
│  kernel NFS client          │◄──────►│  nfsd + tlshd                   │
│  │                          │        │  │                              │
│  ▼                          │        │  │ cert: /var/lib/acme/         │
│  tlshd (client mode)        │        │  │        <server-fqdn>/        │
│  │                          │        │  ▼                              │
│  │ cert: /var/lib/acme/     │        │  /storage/nas/k8s               │
│  │        <node-fqdn>/      │        │  (xprtsec=mtls required)        │
└─────────────────────────────┘        └─────────────────────────────────┘
        ▲                                          ▲
        │          cert issuance (ACME)            │
        └──────────────────────────────────────────┘
                          │
                  ┌───────▼────────┐
                  │  step-ca pod   │
                  │  cert-system   │
                  │  (ACME server) │
                  └────────────────┘
```

### Certificate issuance (ACME HTTP-01)

Each node obtains its own certificate from step-ca using the ACME protocol:

1. `lego` (via NixOS `security.acme`) sends an ACME order to step-ca for the node's FQDN
2. step-ca issues an **HTTP-01** challenge: "serve a token at `http://<node-fqdn>/.well-known/acme-challenge/<token>`"
3. `lego` binds port 80 on the node and serves the token
4. step-ca resolves the node's FQDN via its configured DNS — the node must have a resolvable DNS A record pointing to its LAN IP
5. step-ca fetches the token and marks the challenge passed
6. step-ca issues the certificate, signed by the internal CA root
7. `lego` stores the cert in `/var/lib/acme/<node-fqdn>/`

**EAB (External Account Binding)** is enforced on the step-ca `nixos` ACME provisioner. Every ACME client must present a valid EAB key pair to register, preventing unauthenticated systems from obtaining certs from the internal CA even if they know the ACME URL.

### TLS handshake (NFS mount time)

When a pod mounts an NFS volume with `xprtsec=mtls`:

1. The kernel NFS client contacts `tlshd` (a userspace daemon) via a Unix socket
2. `tlshd` performs the TLS handshake with the NFS server using the node's cert/key
3. The server's `tlshd` validates the client cert against the step-ca root CA
4. The client's `tlshd` validates the server cert against the same root CA
5. If both certs are valid, the TLS session is established and the NFS connection proceeds

The NFS export on the server specifies `xprtsec=mtls`, meaning the server **rejects** connections that do not complete mTLS — including any node that hasn't obtained a client certificate from step-ca.

---

## One-time setup

### 1. Generate EAB credentials

Each node needs its own EAB key pair:

```sh
kubectl exec -n cert-system statefulset/step-ca -- \
  step ca acme eab create nixos
```

Note the `keyID` and `hmacKey` output. Repeat this for each node (NFS server + all k3s clients).

### 2. Get the step-ca root certificate

```sh
kubectl get -n cert-system configmap/step-ca-certs \
  -o jsonpath='{.data.root_ca\.crt}'
```

### 3. Add sops secrets to each node

For each node, add to its `nix/hosts/<hostname>/secrets.yaml`:

```yaml
nfs_acme_env: |
    EAB_KID=<keyID from step 1>
    EAB_HMAC_KEY=<hmacKey from step 1>
    LEGO_CA_CERTIFICATES=/run/secrets/step_ca_root
step_ca_root: |
    -----BEGIN CERTIFICATE-----
    <root cert from step 2>
    -----END CERTIFICATE-----
```

Then re-encrypt:
```sh
sops --encrypt --in-place nix/hosts/<hostname>/secrets.yaml
```

Each node gets its **own unique EAB key pair** (step 1 generates a fresh pair per invocation). The `step_ca_root` value is the same for all nodes.

### 4. Configure nodes in flake.nix

**NFS server node** — enable `serverMode`:
```nix
./nix/modules/nfs-mtls
({config, ...}: {
  sops.secrets.nfs_acme_env = {};
  sops.secrets.step_ca_root = {};

  services.nfs-mtls = {
    enable = true;
    serverMode = true;
    hostname = "<server-fqdn>";  # e.g. nfs.example.com
    acme = {
      environmentFile = config.sops.secrets.nfs_acme_env.path;
      caCertFile = config.sops.secrets.step_ca_root.path;
    };
  };
})
```

**k3s client nodes** — enable `clientMode`:
```nix
./nix/modules/nfs-mtls
({config, ...}: {
  sops.secrets.nfs_acme_env = {};
  sops.secrets.step_ca_root = {};

  services.nfs-mtls = {
    enable = true;
    clientMode = true;
    hostname = "<node-fqdn>";  # e.g. node-0.example.com
    acme = {
      environmentFile = config.sops.secrets.nfs_acme_env.path;
      caCertFile = config.sops.secrets.step_ca_root.path;
    };
  };
})
```

### 5. Deploy

Commit and push the flake changes. ArgoCD will sync the NFS StorageClasses automatically. Deploy the NixOS hosts:

```sh
# Deploy the NFS server first so it's ready before clients try to mount
lab host deploy <nfs-server>

# Then deploy the k3s client nodes
lab host deploy <client-0>
lab host deploy <client-1>
```

On first boot after deploy, the ACME service (`acme-<hostname>.service`) runs and acquires the TLS certificate. `tlshd` starts after the cert is available.

---

## Creating a PVC with the NFS storage class

The `nfs` StorageClass (configured in `k8s/platform/csi-driver-nfs/values.yaml`) mounts from the NFS server with `xprtsec=mtls`. For RWX volumes backed by the NAS:

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

The CSI provisioner creates `/storage/nas/k8s/<pv-uuid>/` on the NFS server automatically. If the PVC is deleted and the StorageClass has `reclaimPolicy: Delete`, the directory is also deleted.

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
| `services.nfs-mtls.serverMode` | bool | false | Run tlshd as NFS server, add xprtsec=mtls exports |
| `services.nfs-mtls.clientMode` | bool | false | Run tlshd as NFS client (for mounting) |
| `services.nfs-mtls.hostname` | str | — | FQDN for this node's certificate |
| `services.nfs-mtls.acme.server` | str | `https://ca.<domain>/acme/nixos/directory` | step-ca ACME URL |
| `services.nfs-mtls.acme.dnsProvider` | str or null | null (HTTP-01) | lego DNS provider; null = use HTTP-01 |
| `services.nfs-mtls.acme.environmentFile` | path | — | sops-decrypted env file (EAB creds + LEGO_CA_CERTIFICATES) |
| `services.nfs-mtls.acme.caCertFile` | path | — | step-ca root CA PEM for tlshd truststore |

**Both `serverMode` and `clientMode` can be enabled simultaneously** on a node that serves NFS to other nodes and also mounts NFS from itself.

---

## ACME challenge type: HTTP-01 vs DNS-01

The module defaults to **HTTP-01** (no `dnsProvider` set). Each node must have a resolvable DNS A record pointing to its LAN IP — step-ca will connect to that IP on port 80 to complete the challenge.

If you prefer **DNS-01** (e.g., to avoid opening port 80), set `acme.dnsProvider = "cloudflare"` and add `CF_DNS_API_TOKEN` to the `environmentFile`. Use a **scoped Cloudflare API token** restricted to `Zone.DNS:Edit` on your zone — this limits the blast radius if the token is ever exposed.

---

## Rotating the step-ca root CA

If the root CA is compromised or you need to replace it:

### 1. Generate a new root CA

```sh
# Inside the step-ca pod
kubectl exec -it -n cert-system statefulset/step-ca -- bash

# Generate a new root — save the output fingerprint
step certificate create "Smallstep Root CA" /tmp/new-root.crt /tmp/new-root.key \
  --profile=root-ca --no-password --insecure

# Bundle it with the old root so both are trusted during the transition
cat /home/step/certs/root_ca.crt /tmp/new-root.crt > /tmp/root-bundle.pem
```

### 2. Cross-sign all existing intermediate CAs with the new root

```sh
# Sign the existing intermediate with the new root
step certificate sign /home/step/certs/intermediate_ca.crt /tmp/new-root.crt \
  /tmp/new-root.key --profile=intermediate-ca > /tmp/new-intermediate.crt
```

### 3. Update the step-ca ConfigMaps

The root and intermediate CA PEMs live in the `step-ca-certs` ConfigMap. Patch it directly to add the new root alongside the old one (keeping both trusted during the transition) and replace the intermediate:

```sh
# Write the new certs to temp files
cat /tmp/new-root.crt >> /tmp/root-bundle.pem       # bundle: old + new root
cat /home/step/certs/root_ca.crt >> /tmp/root-bundle.pem

kubectl create configmap step-ca-certs -n cert-system \
  --from-literal=root_ca.crt="$(cat /tmp/root-bundle.pem)" \
  --from-literal=intermediate_ca.crt="$(cat /tmp/new-intermediate.crt)" \
  --dry-run=client -o yaml | kubectl replace -f -
```

Then restart the pod to pick up the new certs:

```sh
kubectl rollout restart -n cert-system statefulset/step-ca
```

### 4. Update sops secrets on all nodes

Replace `step_ca_root` in each node's `secrets.yaml` with the new root PEM (or the bundle during transition):

```sh
# For each host
sops nix/hosts/<hostname>/secrets.yaml
# Update step_ca_root value, then save and re-encrypt
```

Re-deploy each host to pick up the new truststore:

```sh
deploy .#<hostname>
```

### 5. Force certificate re-issuance

Once all nodes trust the new root, trigger ACME renewal on each node to get certs signed by the new root:

```sh
# On each node
systemctl start acme-<node-fqdn>.service
```

### 6. Remove the old root from the trust bundle

After all nodes have new certs signed by the new root, remove the old root from the step-ca trust bundle and re-deploy. Nodes with certs still signed by the old root will lose connectivity at this point, so ensure all renewals completed first.

---

## Troubleshooting

**Certificate not obtained after deploy**

Check the ACME renewal service:
```sh
# On the affected node
systemctl status acme-<node-fqdn>.service
journalctl -u acme-<node-fqdn>.service -n 50
```

Common causes:
- The `nixos` ACME provisioner isn't active yet — check that the step-ca pod has been restarted after the last ArgoCD sync (see step 1 of setup)
- EAB credentials are wrong or the `nfs_acme_env` secret is missing
- Port 80 is unreachable from step-ca pods (check firewall and DNS A record)

**tlshd not starting**

```sh
systemctl status tlshd.service
journalctl -u tlshd.service -n 50
```

The most common cause is the cert/key not existing yet (ACME hasn't run). Wait for `acme-<hostname>.service` to complete.

**NFS mount fails with "connection refused" or "permission denied"**

```sh
# On the client node, check tlshd is running:
systemctl status tlshd.service

# Verify the xprtsec=mtls export is active on the NFS server:
ssh <nfs-server> exportfs -v

# Check that the kernel TLS module is loaded:
lsmod | grep ^tls
```

**PVC stuck in Pending**

```sh
kubectl describe pvc <pvc-name> -n <namespace>
kubectl logs -n csi-driver-nfs -l app=csi-nfs-controller
```

If the error is TLS-related, ensure the CSI controller pod's node has `clientMode` enabled in `nfs-mtls` and a valid client certificate.
