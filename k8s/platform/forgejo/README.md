# Forgejo

Replaces `k8s/platform/gitea`. Forgejo is a hard fork of Gitea and the official
Helm chart is a fork of the Gitea chart, so most of this is a rename — the
notable divergences are called out below.

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

The webhook stays `gogs`-typed: ArgoCD's `/api/webhook` natively understands the
Gogs payload and reads the shared secret from `argocd-secret`'s
`webhook.gogs.secret`. Forgejo still accepts `gogs` as a hook type. This is
ArgoCD compatibility, not leftover naming.

## Cutover runbook

Ordering matters in one place: nothing may claim `git.msng.to` twice.

`REQUIRE_SIGNIN_VIEW` starts **false** here, unlike the Gitea instance it
replaces. That is the safety margin for the cutover — `ops/k8s-homelab` is
public, so ArgoCD can clone anonymously and step 5 does not depend on
`argocd-repo-k8s-homelab` being right yet. Step 7 turns it back on once that
credential is proven, which is the point at which a broken credential would
otherwise have stranded every Application at once.

The **local Forgejo admin account** (`forgejo-admin-secret`, password sign-in
form enabled) is the fallback throughout — it does not depend on Authelia, LDAP
groups, or any of the renames below.

1. **Point ArgoCD at the GitHub mirror.** Every `repoURL` moves to
   `https://github.com/teekennedy/k8s-homelab`. Push this to Gitea so ArgoCD
   picks it up once, then push to GitHub for everything after. Confirm the
   mirror is current before starting — during the migration window it is the
   only source ArgoCD reads.

   This step must land completely before step 2. Emptying `gitea-resources`
   stops it re-asserting `argocd-repo-k8s-homelab`, but the Secret itself
   survives holding a *Gitea* token for `git.msng.to` — a host that by then
   answers as Forgejo. With `REQUIRE_SIGNIN_VIEW: false` a stale credential is
   no longer fatal (ArgoCD falls back to an anonymous clone of a public repo),
   but a credential that authenticates against nothing is still worth not
   having in the loop while you are changing everything else.

   Files: all 37 `k8s/**/application.yaml`, plus
   `k8s/foundation/argocd/values.yaml` (the `k8s-root` app-of-apps).

2. **Bring up Forgejo; Gitea releases the hostname.** `ingress.enabled: false`
   on Gitea in the same commit that enables Forgejo's, so the two never contend
   for the host, DNS record, or certificate. Gitea stays reachable via
   `kubectl -n gitea port-forward svc/gitea-http 3000:3000`. Its
   `gitea-resources` job is emptied out at the same time — left active it would
   fight forgejo-resources over `argocd-repo-k8s-homelab` and
   `argocd-secret`'s `webhook.gogs.secret`, both of which are keyed on
   `git.msng.to` and now belong to Forgejo.

   Verify before moving on: Valkey has one primary and two replicas
   (`valkey-cli -a … info replication` in `valkey-0`), Sentinel reports the
   primary (`valkey-cli -p 26379 -a … sentinel get-master-addr-by-name
   mymaster`), and you can log into Forgejo as the local admin.

   Gitea's now-unused `gitea-resources-secret-writer` Roles in `argocd`,
   `renovate` and `woodpecker` get pruned by this sync. Forgejo's are named
   `forgejo-resources-*`, so nothing collides — confirm with
   `kubectl get role,rolebinding -A | grep resources-secret-writer`.

   Files: `k8s/platform/forgejo/**` (new), `k8s/platform/gitea/values.yaml`,
   `k8s/foundation/s3-proxy/templates/s3proxy-credentials.yaml`,
   `k8s/platform/monitoring-system/templates/prometheus-rule-maintenance-gates.yaml`.

3. **Run forgejo-resources and verify its output.** It is a post-install hook,
   so it runs on sync. Confirm `argocd-repo-k8s-homelab` exists in `argocd` with
   the right `url` and a working token, and that `woodpecker-forgejo-oauth2`,
   `renovate-forgejo-user`, `homepage-forgejo-credentials` and
   `tkennedy-forgejo-user` are populated. Nothing downstream works until this
   step is green.

   The downstream consumers can be committed alongside step 2 — they simply
   stay broken until the Secrets land. Files:
   `k8s/platform/woodpecker/{values.yaml,application.yaml}`,
   `k8s/platform/renovate/{values.yaml,application.yaml,templates/network-policy.yaml}`,
   `k8s/apps/homepage/{values.yaml,files/homepage-configs/services.yaml}`,
   `renovate.json`, `config/base.cue` + `config/gen/*/env.json`, the
   `gitea-admin-secret` → `forgejo-admin-secret` entries in the shared
   `ignoreDifferences` boilerplate across 7 `application.yaml` files, and the
   doc/CLI-help renames in `cmd/lab/cmd/k8s.go`, `.dagger/README.md`,
   `k8s/foundation/cnpg-system/README.md`, `k8s/apps/redteam/**`.

4. **Flip the identity renames.** Authelia's client `gitea` → `forgejo` (the
   redirect URI `/user/oauth2/authelia/callback` is unchanged — it derives from
   the auth *source* name, still `authelia`, not from the client id), and the
   lldap groups `gitea-admins`/`gitea-users` → `forgejo-*`. The lldap bootstrap
   job runs with `DO_CLEANUP=true`, so the old groups are deleted and the new
   ones created with the same membership in one pass. Re-test OIDC login. If it
   fails, the local admin still works.

   Files: `k8s/platform/auth-system/values.yaml`,
   `k8s/platform/auth-system/files/lldap-bootstrap/{group-configs/service-groups.json,user-configs/humans.json}`.

5. **Point ArgoCD back at `git.msng.to`.** Only after step 3 is confirmed.
   Same file set as step 1, reversed.

6. **Delete `k8s/platform/gitea`.** `platform-root` runs with `prune: true`, so
   removing the directory prunes the Application, and its
   `resources-finalizer` deletes the namespace's resources including PVCs. Also
   drop `gitea` from the reflector allow-list in
   `k8s/foundation/s3-proxy/templates/s3proxy-credentials.yaml` and from the
   `CNPGInstanceNotReady` namespace regex in
   `k8s/platform/monitoring-system/templates/prometheus-rule-maintenance-gates.yaml`
   (both list `forgejo` *and* `gitea` up to this point so the two can coexist).

7. **Turn `REQUIRE_SIGNIN_VIEW` back on.** A one-line follow-up, not one of the
   six migration commits. Do it only once ArgoCD is demonstrably cloning with
   `argocd-repo-k8s-homelab` rather than anonymously — the simplest proof is to
   flip it, then force a refresh on one Application and watch it succeed. If it
   fails, flipping back restores anonymous clone immediately.

### Manual steps this chart does not cover

- **The GitHub push mirror.** `forgejo-resources` has no push-mirror support
  (its `Migrate` field is a *pull* mirror), so the `ops/k8s-homelab` →
  `github.com/teekennedy/k8s-homelab` mirror has to be recreated in the Forgejo
  UI. `k8s/foundation/argocd/values-seed.yaml` bootstraps a fresh cluster from
  that mirror, so it is not optional.
- **Woodpecker state.** Repo ownership is keyed on forge user IDs, which differ
  in a fresh instance. Expect to re-enable repositories, and be ready to wipe
  its volume.
- **Renovate** moves to `platform: "forgejo"` (its own platform, not the Gitea
  one) and re-creates its dependency dashboard on first run.

### Pre-existing bug noticed in passing, not fixed here

`k8s/apps/redteam` claims `ops/k8s-homelab` is "unauthenticated-readable" and
pre-clones it in an init container. `REQUIRE_SIGNIN_VIEW: true` (inherited from
Gitea, kept here) blocks anonymous read regardless of the repo being public, so
that clone has been failing silently — it is guarded with `|| true`. Either give
the redteam pod a read-only token or correct the ROE; out of scope for this
migration.
