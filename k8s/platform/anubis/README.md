# Anubis - AI Bot Firewall

Anubis is a Web AI Firewall that weighs incoming HTTP requests to stop AI crawlers and bots using proof-of-work challenges.

## Architecture

```
Internet → Traefik → Anubis → Backend Service
```

Anubis wraps Traefik ingresses and applies challenges based on request weight (suspicion level).

## Configuration

### Custom Thresholds

The policy defines threshold-based challenges:

- **no-suspicion** (weight ≤ 0): Allow
- **low-suspicion** (0 < weight < 10): Meta refresh challenge (difficulty 1)
- **moderate-suspicion** (10 ≤ weight < 20): Fast PoW (difficulty 2)
- **high-suspicion** (20 ≤ weight < 30): Fast PoW (difficulty 4)
- **extreme-suspicion** (weight ≥ 30): Fast PoW (difficulty 6)

### GeoIP Rules

GeoIP rules add weight to traffic from countries with known bot activity:
- Brazil (BR): +10 weight
- China (CN): +10 weight
- Russia (RU): +10 weight
- India (IN): +10 weight
- Indonesia (ID): +10 weight
- Vietnam (VN): +10 weight

IP ranges are sourced from [ipverse/geo-ip-blocks](https://github.com/ipverse/geo-ip-blocks) and updated daily.

### Debug Logging

Debug logging is enabled via `SLOG_LEVEL:DEBUG` to see weight calculations per request.

## Post-Deployment Setup

### 1. Initialize GeoIP Rules

After first deployment, manually trigger the GeoIP generator:

```bash
# Create a one-time job from the CronJob
kubectl -n anubis create job --from=cronjob/anubis-geoip-generator geoip-init

# Watch the job
kubectl -n anubis logs -f job/geoip-init

# Verify the ConfigMap was updated
kubectl -n anubis get cm anubis-geoip-policy -o yaml | grep "geoip-"
```

The CronJob runs daily at 4 AM UTC to keep IP ranges up to date.

### 2. Verify Anubis is Running

```bash
# Check pods
kubectl -n anubis get pods

# Check metrics
kubectl -n anubis port-forward deployment/anubis-ingress-anubis 9090:9090
curl http://localhost:9090/metrics | grep anubis_policy_results
```

### 3. Apply Anubis to Ingresses

Add to your Ingress spec:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: websecurepublic
spec:
  ingressClassName: anubis  # Use Anubis instead of traefik
  rules:
    - host: app.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-app
                port:
                  number: 80
```

## Monitoring

### View Policy Results

```bash
# Get metrics from Anubis
kubectl -n anubis port-forward deployment/anubis-ingress-anubis 9090:9090
curl http://localhost:9090/metrics | grep anubis_policy_results
```

Example output:
```
anubis_policy_results{action="ALLOW",rule="bot/homelab-internal-networks"} 73
anubis_policy_results{action="ALLOW",rule="threshold/no-suspicion"} 1201
anubis_policy_results{action="CHALLENGE",rule="threshold/moderate-suspicion"} 301
anubis_policy_results{action="WEIGH",rule="bot/geoip-cn"} 45
```

### Check Logs

```bash
# View Anubis controller logs
kubectl -n anubis logs -f deployment/anubis-ingress-anubis

# View Anubis proxy logs (per-ingress pods)
kubectl -n anubis logs -f deployment/ia-homepage

# Filter for weight calculations (debug mode)
kubectl -n anubis logs deployment/ia-homepage | grep weight
```

## Maintenance

### Update GeoIP Countries

Edit `values.yaml`:

```yaml
geoipRuleGenerator:
  enabled: true
  schedule: "0 4 * * *"
  countries:
    - br
    - cn
    - ru
    # Add more ISO 3166-1 alpha-2 country codes
  weight: 10  # Adjust weight added per country
```

Then sync with ArgoCD or run the job manually.

### Manually Trigger GeoIP Update

```bash
kubectl -n anubis create job --from=cronjob/anubis-geoip-generator geoip-manual-$(date +%s)
```

### Adjust Thresholds

Edit `templates/anubis-policy-configmap.yaml` and sync via ArgoCD to update the static policy configuration.

## Troubleshooting

### GeoIP Rules Not Applied

Check the CronJob:
```bash
# View CronJob
kubectl -n anubis get cronjob

# View jobs
kubectl -n anubis get jobs

# Check logs
kubectl -n anubis logs job/anubis-geoip-generator-<timestamp>
```

### GeoIP Rules Not Loading

The `anubis-geoip-policy` ConfigMap is initially empty and populated by the GeoIP generator. ArgoCD is configured to ignore differences in this ConfigMap. After initial deployment, run the manual job above to generate GeoIP rules.

### Challenges Not Working

1. Check Anubis proxy logs for errors
2. Verify the signing key exists: `kubectl -n anubis get secret anubis-signing-key`
3. Check cookie domain is set correctly: `COOKIE_DOMAIN:msng.to`

## References

- [Anubis Documentation](https://anubis.techaro.lol/docs/)
- [GitHub Repository](https://github.com/TecharoHQ/anubis)
- [ingress-anubis Helm Chart](https://github.com/jaredallard/ingress-anubis)
