# oauth2-proxy-instance

A Helm **library** chart holding the homelab-specific glue that every dedicated
oauth2-proxy instance needs. It renders nothing on its own; consumers `include`
its named templates.

## Why a library chart and not a wrapper

`helm dependency build` is not recursive, and `.dagger/scripts/helm-deps.sh`
skips any path matching `*/charts/*` — so a wrapper chart owning the upstream
`oauth2-proxy` dependency would never get that dependency built, and every
consumer's render would fail with a confusing missing-subchart error. Keeping the
upstream dependency in each consumer also keeps its values at the top level
(`oauth2-proxy:`) instead of nested two deep.

The cost is that the upstream chart version is pinned in each consumer's
`Chart.yaml` rather than in one place.

## What it deliberately does NOT own

- **`oauth2-proxy.config.configFile`.** Helm values are static, so a chart cannot
  compute its own subchart's values. The subchart does support
  `config.existingConfig`, but its rollout trigger is
  `checksum/config: {{ include "oauth2-proxy.legacy-config.content" . | sha256sum }}`,
  which does not read an external ConfigMap — using it would silently stop config
  changes from restarting pods. The configs are per-instance anyway (different
  hosts, cookie names, upstreams, group filters, `trusted_ips`).
- **HTTPRoute / Ingress.** The upstream subchart already ships both, behind
  `gatewayApi.enabled` and `ingress.enabled`.

## Usage

Add both dependencies to the consumer's `Chart.yaml`:

```yaml
dependencies:
  - name: oauth2-proxy
    version: 10.7.0
    repository: https://oauth2-proxy.github.io/manifests
  - name: oauth2-proxy-instance
    version: 0.1.0
    repository: file://../../charts/oauth2-proxy-instance
```

Add a one-line `templates/oauth2-proxy-instance.yaml`:

```yaml
{{- include "oauth2ProxyInstance.secret" . }}
{{- include "oauth2ProxyInstance.middleware" . }}
```

And configure it in `values.yaml`:

```yaml
oauth2ProxyInstance:
  # Must equal the Authelia client_id and the auth-system
  # oidcClientSecrets.clients key.
  clientId: copyparty
  # Must equal oauth2-proxy.config.existingSecret, and the auth-system
  # oidcClientSecrets.clients value must point at <namespace>/<this name>.
  secretName: copyparty-oauth2-proxy-secrets
  # Optional: named template rendering the consuming chart's common labels.
  labelsTemplate: copyparty.labels
  middleware:
    # false for reverse-proxy instances (oauth2-proxy owns the hostname).
    enabled: true
    # Must equal oauth2-proxy.fullnameOverride.
    serviceName: copyparty-auth
    authResponseHeaders:
      - X-Auth-Request-User
      - X-Auth-Request-Groups
```

## Adding a new instance

1. Set `oauth2ProxyInstance.clientId` / `secretName` and the matching
   `oauth2-proxy.config.existingSecret`.
2. Add the client to `authelia.configMap.identity_providers.oidc.clients` in
   `k8s/platform/auth-system/values.yaml`, with
   `client_secret.path: /secrets/authelia-oidc-client-secrets/<client_id>`.
3. Add `<client_id>: <namespace>/<secret-name>` to `oidcClientSecrets.clients` in
   the same file. A render-time guard fails `helm template` if steps 2 and 3
   disagree.
4. Add an `ignoreDifferences` entry for the Secret (`jsonPointers: [/data]`) to
   the consuming chart's `application.yaml`, so ArgoCD doesn't fight mittwald.
