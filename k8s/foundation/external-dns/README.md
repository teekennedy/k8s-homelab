# External DNS -- Split DNS Architecture

This chart implements split DNS for the homelab using three external-dns instances in a single namespace. Internal clients resolve services directly to cluster IPs (avoiding NAT reflection), while external clients resolve to the public IP.

## Architecture

All three instances run in the `external-dns` namespace.

### CloudFlare instance (public DNS)

- **Provider:** CloudFlare
- **Scope:** Only services annotated with `traefik.ingress.kubernetes.io/router.entrypoints: wspublic`
- **Record type:** A records pointing to the home network's public IP
- **How it works:** The DDNS updater periodically checks the public IP and writes it to the `ddns-public-ip` ConfigMap. External-dns reads this as `--default-targets` and creates A records in CloudFlare for all discovered external services.

### Internal instance (Unifi DNS -- non-public services)

- **Provider:** Unifi DNS via [external-dns-unifi-webhook](https://github.com/kashalls/external-dns-unifi-webhook)
- **Scope:** Services _not_ annotated with `wspublic` (annotation filter: `notin`)
- **Record type:** A records pointing to the actual cluster/MetalLB IPs (auto-discovered from load balancer resources)
- **How it works:** The Unifi webhook sidecar creates DNS records in the Unifi controller. Internal clients resolve directly to cluster IPs.

### Public-internal instance (Unifi DNS -- public services)

- **Provider:** Unifi DNS via the same webhook
- **Scope:** Only services annotated with `wspublic` (annotation filter: `in`)
- **Record type:** A records pointing to the public MetalLB VIP (`10.69.80.120`) via `--default-targets` / `--force-default-targets`
- **How it works:** Creates Unifi DNS records for public services so LAN clients reach them via the MetalLB VIP instead of going through NAT reflection.

### DDNS Updater

- Runs as a Deployment in the `external-dns` namespace
- Polls `https://checkip.amazonaws.com` every 5 minutes for the current public IP
- When the IP changes, it patches the `ddns-public-ip` ConfigMap and triggers a rollout restart of the CloudFlare external-dns deployment so it picks up the new `--default-targets` value
- Uses a Kubernetes ServiceAccount with Role/RoleBinding scoped to the `external-dns` namespace

## Setup

### CloudFlare

1. The CloudFlare API token is managed by terraform in `terraform/cloudflare/main.tf`. Run `terraform apply` there to create the `cloudflare-api-token` Secret in the `external-dns` namespace.
2. Deploy via ArgoCD (automatic from this chart directory).

### Unifi (internal instances)

1. Create a Unifi API key in the Unifi console:
   - Log in at https://unifi.ui.com and select your network
   - Go to Settings > Control Plane > Integrations
   - Create a new API key
2. Store the API key in `terraform/lan/tfvars.sops.yaml` (see `tfvars.sops.example.yaml` for format)
3. Encrypt with `sops encrypt --in-place terraform/lan/tfvars.sops.yaml`
4. Run `terraform apply` in `terraform/lan/` to create the `unifi-api-key` Secret in the `external-dns` namespace
5. Deploy via ArgoCD (automatic from this chart directory)

## Adding a new service

### External (public) service

Add this annotation to your Ingress or IngressRoute:

```yaml
annotations:
  traefik.ingress.kubernetes.io/router.entrypoints: wspublic
```

This tells Traefik to route via the public entrypoint. The CloudFlare instance creates a public A record, and the public-internal Unifi instance creates an internal A record pointing to the MetalLB VIP.

### Internal-only service

No special annotations needed. The internal Unifi instance discovers all non-`wspublic` services automatically and creates A records pointing to the cluster IPs.

## IPv6

IPv6 is currently disabled in the k3s cluster. When enabling:

- **Internal:** The Unifi webhook auto-discovers IPv6 addresses from dual-stack services
- **External:** The DDNS updater would need to detect the IPv6 public address and the external-dns `--default-targets` would need a second entry for the AAAA address
