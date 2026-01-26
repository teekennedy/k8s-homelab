# Kured Webhook Changes

## Issues Fixed

### 1. TLS Handshake Errors
**Problem**: Kured was trying to make HTTPS connections to the webhook, causing "Bad request version" errors.

**Root Cause**: The `generic://` URL scheme in kured's shoutrrr notification library was being converted to `https://` instead of `http://`.

**Fix**: Changed `notifyUrl` from `generic://` to `http://` in values.yaml.

**Changes**:
- Updated webhook to suppress TLS handshake error logs
- Added GET handler for health checks at `/health`, `/healthz`, and `/`
- Changed kured notifyUrl from `generic://kured-webhook...?template=json` to `http://kured-webhook...`

## New Features

### 2. Longhorn Automatic Eviction
**Feature**: Webhook now automatically triggers Longhorn node and disk eviction when kured drains a node.

**How it works**:
1. When kured sends `event=drain`, webhook calls Longhorn API to:
   - Set node `allowScheduling: false` and `evictionRequested: true`
   - Set all disks to `allowScheduling: false` and `evictionRequested: true`
2. Longhorn automatically evacuates replicas to other nodes
3. When replicas are evacuated, instance manager PDB is removed
4. Kured can now drain the node successfully
5. After reboot, when kured sends `event=uncordon`, webhook restores Longhorn scheduling

**Changes**:
- Added Kubernetes API client functions to server.py
- Added `evict_longhorn_node()` and `restore_longhorn_node()` functions
- Created ServiceAccount and RBAC resources for webhook
- Updated deployment to use ServiceAccount

## Files Modified

- `files/kured-webhook/server.py` - Enhanced with K8s API client and Longhorn eviction logic
- `templates/kured-webhook-deployment.yaml` - Added serviceAccountName
- `templates/kured-webhook-rbac.yaml` - **NEW** - RBAC for Longhorn access
- `values.yaml` - Fixed notifyUrl scheme
- `LONGHORN_EVICTION.md` - **NEW** - Documentation
- `CHANGES.md` - **NEW** - This file

## Deployment

Since you're using ArgoCD:

```bash
git add k8s/foundation/kured/
git commit -m "Fix kured webhook TLS errors and add Longhorn eviction support"
git push
```

ArgoCD will automatically sync and deploy the changes.

## Testing

After deployment:

```bash
# Verify webhook is running with new code
kubectl -n kured get pods -l app.kubernetes.io/name=kured-webhook
kubectl -n kured logs -f deployment/kured-webhook

# Test health endpoint
kubectl -n kured run -it --rm debug --image=curlimages/curl --restart=Never -- \
  curl -v http://kured-webhook.kured.svc.cluster.local:8080/health

# Trigger a reboot and watch it work
ssh <node> sudo touch /var/run/reboot-required

# Watch the logs
kubectl -n kured logs -f deployment/kured-webhook
# Should see: "Requesting Longhorn eviction for node <nodename>"
```

## Expected Behavior After Fix

**Before**:
- TLS handshake errors in webhook logs
- Manual Longhorn eviction required before node reboot

**After**:
- No TLS errors (clean logs)
- Fully automated: `touch /var/run/reboot-required` → webhook evicts Longhorn → kured drains → node reboots → Longhorn scheduling restored
