# CrowdSec - Collaborative Security Engine

CrowdSec is a free, open-source security engine that detects and blocks malicious actors using collaborative threat intelligence.

## Architecture

```
Internet → Traefik (CrowdSec Plugin) → Anubis → Backend
           ↓
           CrowdSec LAPI ← CrowdSec Agent (reads Traefik logs)
```

**Defense in Depth**:
1. **CrowdSec**: Blocks known bad IPs at Traefik (fast, binary decision)
2. **Anubis**: Handles behavioral bot detection for traffic that passes CrowdSec

## How It Works

1. **Agent**: Parses Traefik access logs and detects attacks (brute force, web scans, etc.)
2. **LAPI**: Receives alerts from agents and makes ban decisions
3. **Bouncer**: Traefik plugin that blocks IPs based on LAPI decisions
4. **Community Blocklist**: (Optional) Subscribe to global threat intelligence

## Components

- **CrowdSec LAPI**: Local API server that stores decisions (1Gi PVC on Longhorn)
- **CrowdSec Agent**: DaemonSet that monitors Traefik pod logs
- **Traefik Bouncer Plugin**: Runs inside Traefik, blocks bad IPs
- **Bootstrap Job**: Post-install job that configures the bouncer automatically

## Post-Deployment Setup

### Automated Bootstrap

The bouncer setup is **fully automated** via a Kubernetes Job that runs after deployment. The job:

1. ✅ Generates a bouncer API key from CrowdSec LAPI
2. ✅ Updates the Traefik middleware with the key
3. ✅ Restarts Traefik to apply changes
4. ✅ Is idempotent (safe to re-run)

**Monitor the bootstrap job:**

```bash
# Watch the job
kubectl -n crowdsec get jobs -w

# View job logs
kubectl -n crowdsec logs -f job/crowdsec-bootstrap-middleware
```

**Expected output (first run):**
```
CrowdSec Bouncer Middleware Bootstrap Starting...
Loaded in-cluster Kubernetes configuration
Middleware has placeholder key, needs configuration
Found LAPI pod: crowdsec-lapi-xxx
Generating bouncer key in pod crowdsec-lapi-xxx...
Successfully generated bouncer key
Updating middleware crowdsec-bouncer...
Successfully updated middleware
Restarting Traefik deployment...
Triggered Traefik restart, waiting for rollout...
Traefik rollout completed successfully
Bootstrap completed successfully
```

**Expected output (subsequent runs):**
```
CrowdSec Bouncer Middleware Bootstrap Starting...
Loaded in-cluster Kubernetes configuration
Middleware already configured with a valid bouncer key
Bootstrap completed (already configured)
```

The job is **idempotent** - it detects if the middleware already has a valid key and skips generation to avoid creating duplicate bouncers.

### Verify Bouncer Connection

Check that the bouncer is connected:

```bash
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli bouncers list
```

Expected output:
```
Name              IP Address      Valid  Last API pull         Type                       Version  Auth Type
traefik-bouncer   10.42.x.x       ✔️      2026-01-23T14:30:00Z  crowdsec-bouncer-traefik          api-key
```

### Apply Middleware to Ingresses

**For Ingress resources**, add the annotation:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: crowdsec-crowdsec-bouncer@kubernetescrd
spec:
  # ... rest of ingress
```

**For IngressRoute resources**, add to the route:

```yaml
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: my-app
spec:
  routes:
    - match: Host(`app.example.com`)
      kind: Rule
      middlewares:
        - name: crowdsec-bouncer
          namespace: crowdsec
      services:
        - name: my-app
          port: 80
```

## Monitoring

### Check Decisions (Bans)

```bash
# List active bans
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli decisions list

# Check metrics
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli metrics
```

### View Agent Activity

```bash
# Check which scenarios are triggering
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli alerts list

# View specific alert
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli alerts inspect <alert-id>
```

### Test the Bouncer

Simulate an attack to verify CrowdSec is working:

```bash
# Manually add a test ban
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli decisions add --ip 1.2.3.4 --duration 5m

# Try accessing your site from that IP (or check with curl)
curl -H "X-Forwarded-For: 1.2.3.4" https://msng.to

# Remove the test ban
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli decisions delete --ip 1.2.3.4
```

### Check Traefik Plugin Logs

```bash
# Traefik logs with plugin debug output
kubectl -n kube-system logs -f deployment/traefik | grep -i crowdsec
```

## Optional: CrowdSec Console Enrollment

The [CrowdSec Console](https://app.crowdsec.net) provides centralized management and community blocklists. It's **free** but requires signup.

### Benefits:
- View all your instances in one dashboard
- Subscribe to community blocklists (shared threat intel)
- Analytics and reporting
- Centralized alerts

### Enroll Your Instance:

1. Sign up at https://app.crowdsec.net (free)
2. Create an enrollment key in the console
3. Edit `values.yaml`:

```yaml
crowdsec:
  lapi:
    env:
      - name: ENROLL_KEY
        valueFrom:
          secretKeyRef:
            name: crowdsec-secrets
            key: enrollKey
      - name: ENROLL_INSTANCE_NAME
        value: "k8s-homelab"
```

4. Sync with ArgoCD or upgrade the Helm release

## Configuration

### Disable Bootstrap Job

If you want to configure the bouncer manually, disable the bootstrap job in `values.yaml`:

```yaml
bootstrapMiddleware:
  enabled: false
```

### Adjust Stream Mode Update Interval

Edit `values.yaml`:

```yaml
traefikBouncer:
  mode: stream
  updateInterval: 60  # seconds (default 60)
```

Faster updates = higher LAPI load, slower updates = delayed ban enforcement.

### Change Default Action

```yaml
traefikBouncer:
  defaultAction: ban  # or "captcha"
```

### Enable Debug Logging

```yaml
traefikBouncer:
  logLevel: DEBUG
```

Then check Traefik logs for detailed plugin output.

### Add More Collections

Collections are sets of parsers and scenarios for specific services:

```yaml
crowdsec:
  agent:
    env:
      - name: COLLECTIONS
        value: "crowdsecurity/traefik crowdsecurity/http-cve crowdsecurity/base-http-scenarios crowdsecurity/wordpress"
```

Browse available collections: https://hub.crowdsec.net/browse/#collections

## Troubleshooting

### Bouncer Shows as Invalid

If `cscli bouncers list` shows "Invalid" or bouncer isn't listed:

**Option 1: Re-run the bootstrap job (recommended)**

Delete the job to trigger a re-run on next sync:

```bash
kubectl -n crowdsec delete job crowdsec-bootstrap-middleware
argocd app sync crowdsec
```

**Option 2: Manual setup**

If the bootstrap job fails, you can set up the bouncer manually:

1. Generate a bouncer API key:
   ```bash
   kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli bouncers add traefik-bouncer -o raw
   ```

2. Update the middleware with the key:
   ```bash
   kubectl -n crowdsec edit middleware crowdsec-bouncer
   ```

   Find `CrowdsecLapiKey: "REPLACE_WITH_REAL_KEY"` and replace with the generated key.

3. Restart Traefik:
   ```bash
   kubectl -n kube-system rollout restart deployment traefik
   ```

### Agent Not Detecting Attacks

Check agent logs:

```bash
# View agent logs
kubectl -n crowdsec logs -f daemonset/crowdsec-agent

# Verify log acquisition
kubectl -n crowdsec exec daemonset/crowdsec-agent -- cat /etc/crowdsec/acquis.yaml
```

Ensure Traefik access logs are enabled (configured in `templates/traefik-config.yaml`).

### Plugin Not Loading in Traefik

Check Traefik startup logs:

```bash
kubectl -n kube-system logs deployment/traefik | grep -i plugin
```

The CrowdSec plugin is configured in `nix/modules/k3s/k3s.nix` as part of the Traefik HelmChartConfig. If you see "plugin not found", verify the plugin configuration is present in the Nix config and rebuild the system.

### Bootstrap Job Fails

Check the job logs for errors:

```bash
kubectl -n crowdsec logs job/crowdsec-bootstrap-middleware
```

Common issues:
- **LAPI pod not ready**: Wait for LAPI to start, then delete the job to retry
- **Middleware not found**: Ensure the middleware resource was created
- **Permission errors**: Check RBAC resources (ServiceAccount, Role, ClusterRole)

The bootstrap script source is in `files/bootstrap-middleware/` if you need to debug or modify it.

### High False Positive Rate

Whitelist trusted IPs:

```bash
# Whitelist your home IP
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli decisions add --ip <your-ip> --type whitelist

# Or edit the parsers to be less aggressive
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli parsers list
```

## Maintenance

### Update Collections

```bash
# Update the hub index
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli hub update

# Upgrade all collections
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli hub upgrade
```

### View Database Size

```bash
# Check PVC usage
kubectl -n crowdsec exec deployment/crowdsec-lapi -- df -h /var/lib/crowdsec/data
```

The LAPI database stores decisions and alerts. With a 1Gi PVC, it should last months before needing cleanup.

## References

- [CrowdSec Documentation](https://docs.crowdsec.net/)
- [CrowdSec Hub](https://hub.crowdsec.net/) (collections, parsers, scenarios)
- [Traefik Bouncer Plugin](https://github.com/maxlerebourg/crowdsec-bouncer-traefik-plugin)
- [CrowdSec Helm Chart](https://github.com/crowdsecurity/helm-charts)
- [Kubernetes Integration Guide](https://www.crowdsec.net/blog/kubernetes-crowdsec-integration)

## Cost

**100% Free**:
- CrowdSec Security Engine: Free & open-source
- Local API: Free
- Bouncers: Free
- CrowdSec Console: Free (optional)
- Community blocklists: Free (requires console enrollment)

No paid plans required for core functionality.
