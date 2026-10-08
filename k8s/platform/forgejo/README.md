# Forgejo

Forgejo is a hard fork of Gitea and the official Helm chart is a fork of
the Gitea chart, so most of this is a rename — the notable divergences are
called out below.

## What differs from the Gitea chart it replaces

**Valkey is not bundled.** The Forgejo chart dropped the `valkey-cluster`
dependency the Gitea chart had (`Chart.lock` here has only `common`), so
cache/session storage is ours to provide. `templates/valkey-*.yaml` is a port of
the replication-set + Sentinel setup from `k8s/platform/auth-system`, running as
a **separate instance** rather than sharing Authelia's:

- `maxmemory-policy` is instance-wide, and Forgejo's cache is exactly the kind
  of churn that would start evicting Authelia's session keys under pressure.
- A Valkey incident shouldn't take out login and git simultaneously.

It uses `longhorn-rc1` (single replica) because Valkey's own replication is the
redundancy; cross-node Longhorn replication would only add write amplification.
Note `auth-system`'s equivalent still uses plain `longhorn` — deliberately not
changed here, but worth revisiting.

**The queue stays on leveldb, not Valkey.** `[queue] TYPE=level`. The Valkey
instance runs `allkeys-lru`, and an evicted queue key is a silently lost job.
Forgejo is a single replica with a persistent volume, so the local leveldb queue
is both correct and durable.

**Connection strings are assembled by Kubernetes, not committed.** Forgejo takes
Valkey as a `redis+sentinel://` URI with the password inline, and neither
`app.ini` nor `environment-to-ini` interpolates secrets. So
`forgejo.gitea.additionalConfigFromEnvs` declares `VALKEY_PASSWORD` and
`SENTINEL_PASSWORD` first, then references them as `$(VALKEY_PASSWORD)` in the
URI — Kubernetes expands `$(VAR)` from env vars declared *earlier in the same
container's list*, so **the order of that list is load-bearing**. The passwords
are hex-encoded (`valkey-secret.yaml`) so they can never contain a character
that would corrupt the URI. These vars land only in the `init-app-ini` init
container, so the running Forgejo container carries no credentials.

Query-parameter spelling is `mastername` — Forgejo strips `_` and `-` from query
keys, so `master_name` also works, but `mastername` is canonical.

**`gitea` is still the values key.** The upstream chart kept `gitea:` for its
app.ini block, the `GITEA__*` env prefix (though `FORGEJO__*` is also accepted
and is what this chart uses), and the `gitea` CLI name. The main container is
named after the chart, i.e. `forgejo` — that is what
`forgejo-resources.podExec.container` refers to.

## forgejo-resources

`files/config` is the Gitea job ported over. Two things became config rather than
hardcoded values:

- `oauth2Apps[].clientIDKey` / `.clientSecretKey` — the consuming app's env var
  names (Woodpecker reads `WOODPECKER_FORGEJO_CLIENT`/`_SECRET` straight out of
  the Secret via `envFrom`), so they belong to the consumer, not to us.
- `podExec.container`.

### Repository webhooks

`repositories[].webhooks` is a list; each entry is reconciled by its URL on
every job run.

```yaml
webhooks:
  - url: https://…
    type: gogs | gitea   # required, no default
    events: [push]       # optional, defaults to [push]
    branchFilter: main   # optional; only constrains push events
    secretName: …        # the Secret that holds (or will hold) the shared secret
    secretNamespace: …
    secretKey: …
```

**`type` is required on purpose.** It selects both the payload format and the
signature header the receiver verifies — `gogs` signs `X-Gogs-Signature`,
`gitea` signs `X-Gitea-Signature`. A wrong value registers happily and then
fails every delivery at the far end, which is a miserable thing to debug from
this side. Only those two are accepted; the SDK's chat types (slack, discord, …)
take entirely different `Config` keys and nothing here populates them.

The ArgoCD hook stays `gogs`-typed: ArgoCD's `/api/webhook` natively
understands the Gogs payload and reads the shared secret from `argocd-secret`'s
`webhook.gogs.secret`. That is ArgoCD compatibility, not leftover naming.
OpenHands' hook (`k8s/apps/openhands`) is `gitea`-typed and subscribes to the
comment events its automation event source wakes on.

Reconcile semantics, since they are easy to get wrong:

- Matching is by `Config["url"]` — the SDK's `Hook.URL` field is `json:"-"` and
  is never populated from the API.
- Events, `branchFilter` and `active` are converged on every pass, so changing
  `events:` in values actually takes effect on an existing hook. (The previous
  single-hook form was create-only and silently ignored later edits.)
- The shared secret is rewritten every pass too: Forgejo never returns it, so it
  cannot be compared, and rewriting is the only way a rotation in the Secret
  reaches the hook.
- A `type:` change is a delete-and-recreate — the Forgejo API has no way to
  PATCH a hook's payload format.
- The target Secret must already exist; it is patched additively, so unrelated
  keys (argocd-secret's own auto-managed ones) are untouched. On a cold
  bootstrap the Job may run before a consuming app's namespace exists — it logs
  that one hook and retries on the next sync.

Config problems (`type` missing or unknown, duplicate URLs, missing secret
coordinates, or the retired singular `webhook:` key) are collected and the job
exits **before** touching Forgejo, rather than the usual log-and-continue —
partial reconciliation of a malformed hook is worse than none. `webhook_test.go`
pins that behaviour, and the chart's own template `fail`s on the singular key so
the error usually surfaces at render time instead.

### Manual steps this chart does not cover

- **The GitHub push mirror.** `forgejo-resources` has no push-mirror support
  (its `Migrate` field is a *pull* mirror), so the `ops/k8s-homelab` →
  `github.com/teekennedy/k8s-homelab` mirror has to be recreated in the Forgejo
  UI. `k8s/foundation/argocd/values-seed.yaml` bootstraps a fresh cluster from
  that mirror, so it is not optional.
- **Woodpecker state.** Repo ownership is keyed on forge user IDs, which differ
  in a fresh instance. Expect to re-enable repositories, and be ready to wipe
  its volume.
