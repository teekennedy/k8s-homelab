# CrowdSec - Collaborative Security Engine

CrowdSec is a free, open-source security engine that detects and blocks malicious actors using collaborative threat intelligence.

## Architecture

```
Internet → Traefik (CrowdSec Plugin) ──────────────→ Backend
             │  ↑ decisions (pulled every 60s)
             │  CrowdSec LAPI ← CrowdSec Agent (reads Traefik logs)
             │       ↑ alerts
             └→ CrowdSec AppSec (WAF)
```

**How it works**: CrowdSec blocks known bad IPs at Traefik (fast, binary decision).
Each request is also checked by the AppSec component (WAF): targeted exploit
rules block in-line, and the broader OWASP CRS runs afterwards and only
contributes to bans.

## How It Works

1. **Agent**: Parses Traefik access logs and detects attacks (brute force, web scans, etc.)
2. **AppSec**: Runs WAF rules (virtual patching, generic rules, OWASP CRS) against request headers and bodies
3. **LAPI**: Receives alerts from agents and AppSec and makes ban decisions
4. **Bouncer**: Traefik plugin that blocks IPs based on LAPI decisions
5. **Community Blocklist**: (Optional) Subscribe to global threat intelligence

## Components

- **CrowdSec LAPI**: Local API server that stores decisions (1Gi PVC on Longhorn)
- **CrowdSec Agent**: DaemonSet that monitors Traefik pod logs
- **CrowdSec AppSec**: 2-replica Deployment serving the WAF on port 7422
- **Traefik Bouncer Plugin**: Runs inside Traefik, blocks bad IPs

## Bouncer API Key

The bouncer key needs no setup:

1. kubernetes-secret-generator fills the `crowdsec-bouncer-key` Secret (`apiKey`).
2. LAPI registers a bouncer named `traefik` with that key at startup, from the
   `BOUNCER_KEY_traefik` env var.
3. The Middleware sets `CrowdsecLapiKey: urn:k8s:secret:crowdsec-bouncer-key:apiKey`,
   and Traefik substitutes the Secret's value when it builds the middleware.

LAPI only registers a bouncer whose name doesn't exist yet. If the Secret is
regenerated while the LAPI database survives, delete the stale bouncer and
restart LAPI:

```bash
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli bouncers delete traefik
kubectl -n crowdsec rollout restart deployment crowdsec-lapi
```

### Verify Bouncer Connection

Check that the bouncer is connected:

```bash
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli bouncers list
```

Expected output:
```
Name              IP Address      Valid  Last API pull         Type                       Version  Auth Type
traefik           10.42.x.x       ✔️      2026-01-23T14:30:00Z  crowdsec-bouncer-traefik          api-key
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

Traefik sees the real client IP (the public Service uses
`externalTrafficPolicy: Local`), so a spoofed `X-Forwarded-For` header has no
effect. Test from a machine outside the LAN, such as a phone on mobile data:

```bash
# Ban the external IP of the test device
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli decisions add --ip <test-device-ip> --duration 5m

# Load any public site from that device; expect a 403 within updateInterval (60s)

# Remove the test ban
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli decisions delete --ip <test-device-ip>
```

### Check Traefik Plugin Logs

```bash
# Traefik logs with plugin debug output
kubectl -n kube-system logs -f deployment/traefik | grep -i crowdsec
```

## AppSec (WAF)

The Traefik plugin sends every request to the AppSec component before passing
it on. The `homelab/appsec` config in `values.yaml` splits the rules in two:

- **In-band** (blocking): `crowdsecurity/vpatch-*` (virtual patches for
  specific CVEs) and `crowdsecurity/generic-*`. These are narrow, cheap and
  rarely misfire, so a match returns a 403 for that request.
- **Out-of-band** (async): the OWASP Core Rule Set (CRS), a broad,
  signature-based rule set for SQL injection, XSS, path traversal, RCE and
  similar attacks. It is noisier and more expensive, so AppSec replies to the
  bouncer before running it. A CRS match never blocks a request; it only
  counts toward a ban.

| Scenario | Rules | Bans after |
|---|---|---|
| `crowdsecurity/appsec-vpatch` | In-band matches | 2 different rules within 60s |
| `crowdsecurity/crowdsec-appsec-outofband` | Out-of-band (CRS) matches | 6 matches within ~30s |

A ban reaches Traefik on its next decision pull (`updateInterval`).

### Cost

Measured with CrowdSec v1.8.1 limited to 1 CPU. "Reply" is the time the
bouncer waits; CRS time is spent after the reply.

| Request body | Reply | CRS (async) |
|---|---|---|
| None | ~2ms | <1ms |
| 64 KiB or larger (truncated to 64 KiB) | 2-4ms | <1ms |

Only the first 64 KiB of a body is sent (`traefikBouncer.appsec.bodyLimit`),
which caps the in-band cost (it was ~5ms at 256 KiB and ~20ms at 1 MiB). The
plugin holds that much of an upload in memory before forwarding it. CRS skips body inspection for content types it can't
parse (`homelab/crs-skip-unparsed-body`); without that, scanning a 1 MiB
`application/octet-stream` upload took 9s of CPU.

Each AppSec routine finishes a request's CRS pass before taking the next
request, so sustained heavy traffic can queue requests behind CRS work.

The plugin's AppSec settings all fail open. If AppSec is down, returns an
error, or gets a body it can't buffer, the request passes through.

### Operations

```bash
# AppSec hits, by rule
kubectl -n crowdsec exec deployment/crowdsec-appsec -- cscli metrics show appsec

# Bans from AppSec scenarios
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli alerts list -s crowdsecurity/appsec-vpatch
kubectl -n crowdsec exec deployment/crowdsec-lapi -- cscli alerts list -s crowdsecurity/crowdsec-appsec-outofband
```

If a rule falsely flags an app's traffic, disable it by ID in `homelab/appsec`:

```yaml
on_load:
  - apply:
      - RemoveOutBandRuleByID(942100)   # CRS rule
      - RemoveInBandRuleByName("crowdsecurity/vpatch-env-access")
```

The rule ID or name is in `cscli alerts inspect -d <alert-id>`.

If CRS misflags an app's normal traffic across many rules, skip CRS for that
host in `homelab/crs-skip-hosts` instead. Jellyfin is skipped this way: its
playback progress reports and web UI bundles got streaming clients banned.

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

If `cscli bouncers list` shows "Invalid" or the `traefik` bouncer isn't listed:

- Check that the `crowdsec-bouncer-key` Secret has an `apiKey`.
- Check the LAPI startup logs for `Registered bouncer for traefik`.
- If the key changed, follow [Bouncer API Key](#bouncer-api-key) to
  re-register it.
- Check the Traefik logs for errors resolving the `urn:k8s:secret:` reference.
  Traefik drops a middleware whose Secret it can't read.

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
