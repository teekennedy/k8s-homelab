# External DNS -- Split DNS Architecture

This chart and its companion `external-dns-internal` implement split DNS for the homelab. The goal is to ensure that internal clients resolve services directly to cluster IPs (avoiding NAT reflection), while external clients resolve to the public IP.

## Architecture

### External instance (this chart -- CloudFlare)

- **Provider:** CloudFlare
- **Scope:** Only services annotated with `traefik.ingress.kubernetes.io/router.entrypoints: websecurepublic`
- **Record type:** A records pointing to the home network's public IP
- **How it works:** The DDNS updater periodically checks the public IP and writes it to the `ddns-public-ip` ConfigMap. External-dns reads this as `--default-targets` and creates A records in CloudFlare for all discovered external services.

### Internal instance (`external-dns-internal` chart -- Unifi)

- **Provider:** Unifi DNS via [external-dns-unifi-webhook](https://github.com/kashalls/external-dns-unifi-webhook)
- **Scope:** All services (no annotation filter)
- **Record type:** A records pointing to the actual cluster/MetalLB IPs (auto-discovered from load balancer resources)
- **How it works:** The Unifi webhook sidecar runs alongside external-dns and creates DNS records in the Unifi controller's DNS service. Internal clients resolve directly to cluster IPs.

### DDNS Updater

- Runs as a Deployment in the `external-dns` namespace
- Polls `https://checkip.amazonaws.com` every 5 minutes for the current public IP
- When the IP changes, it patches the `ddns-public-ip` ConfigMap and triggers a rollout restart of the external-dns deployment so it picks up the new `--default-targets` value
- Uses a Kubernetes ServiceAccount with Role/RoleBinding scoped to the `external-dns` namespace

## Setup

### External instance (CloudFlare)

1. The CloudFlare API token is managed by terraform in `terraform/cloudflare/main.tf`. Run `terraform apply` there to create the `cloudflare-api-token` Secret in the `external-dns` namespace.
2. Deploy via ArgoCD (automatic from this chart directory).

### Internal instance (Unifi)

1. Create a Unifi API key in the Unifi console:
   - Log in at https://unifi.ui.com and select your network
   - Go to Settings > Control Plane > Integrations
   - Create a new API key
2. Store the API key in `terraform/lan/tfvars.sops.yaml` (see `tfvars.sops.example.yaml` for format)
3. Encrypt with `sops encrypt --in-place terraform/lan/tfvars.sops.yaml`
4. Run `terraform apply` in `terraform/lan/` to create the `unifi-api-key` Secret in the `external-dns-internal` namespace
5. Deploy via ArgoCD (automatic from the `external-dns-internal` chart directory)

## Adding a new service

### External (public) service

Add these annotations to your Ingress or IngressRoute:

```yaml
annotations:
  traefik.ingress.kubernetes.io/router.entrypoints: websecurepublic
```

This tells both Traefik to route via the public entrypoint, and external-dns to create a public A record in CloudFlare. The internal external-dns instance will also auto-discover the service and create an internal A record in Unifi DNS.

### Internal-only service

No special annotations needed. The internal external-dns instance discovers all services automatically and creates A records in Unifi DNS pointing to the cluster IPs.

## Post-migration cleanup

After confirming split DNS is working:

1. Delete the orphaned `external.network.msng.to` A record from CloudFlare (it was previously managed by the DDNS updater directly)
2. Remove the `cloudflare-zone` ConfigMap from the `external-dns` namespace if it still exists (terraform resource was removed)

## IPv6

IPv6 is currently disabled in the k3s cluster. When enabling:

- **Internal:** The Unifi webhook auto-discovers IPv6 addresses from dual-stack services
- **External:** The DDNS updater would need to detect the IPv6 public address and the external-dns `--default-targets` would need a second entry for the AAAA address
