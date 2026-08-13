# freshrss

Self-hosted feed reader using OIDC auth.

Upstream docs: <https://freshrss.github.io/FreshRSS/>

## Auth

OIDC auth requires the user to be in either the `freshrss-users` or
`freshrss-admins` group in order to access any page of the application.
Anonymous access is not allowed, and no other login method is supported.

The FreshRSS Debian image ships `libapache2-mod-auth-openidc`, and its vhost puts
`AuthType openid-connect` / `Require valid-user` on the entire application directory.
An unauthenticated request is redirected into the OIDC flow by Apache and never
reaches PHP until the flow is complete.

FreshRSS runs with `--auth-type http_auth`: it trusts `REMOTE_USER`, which
`mod_auth_openidc` sets from the `preferred_username` claim. The OIDC server,
Authelia, is configured with multiple groups providing different levels of
access. The `freshrss` authorization policy in
`k8s/platform/auth-system/values.yaml`:

| group | Policy |
| --- | --- |
| `freshrss-users` | `one_factor` |
| `freshrss-admins` | `two_factor` |
| `full-admin` | `two_factor` |

Anything else is denied. Note that FreshRSS has no group→role mapping. The
second group exists so that the distinction (and its stronger 2FA requirement)
is expressible without hand-editing this chart later.

New accounts are created on first login (FreshRSS's `http_auth_auto_register`
default). That is the only signup path, and it is gated entirely by group
membership.

### Admin

FreshRSS grants the admin pages to `default_user === currentUser || is_admin`
(`app/Models/Auth.php`). `default_user` is therefore implicitly an admin, and
FreshRSS refuses to delete it — so naming a real person there would weld the
admin bit onto that account for good.

Instead, `--default-user` is **`freshrss-admin`, a placeholder with no IdP
identity**. The FreshRSS account exists (`FRESHRSS_USER` creates it, or the
install fails on a missing default user), but since login is entirely
`REMOTE_USER`-driven and no IdP identity maps to that name, nobody can ever log
in as it. It just occupies the undeletable slot. Human accounts are then all
ordinary and freely deletable, and admin is granted per account with `is_admin`,
which defaults to false — including for accounts created by auto-registration.

Admin unlocks user management, the global configuration (auth type, anonymous
access, API), extension management for all users, the system log, and the
update/about pages. Everything else — feeds, categories, reading, per-user
settings, API password — is ordinary user scope.

**Granting admin.** No CLI exposes `is_admin`: `reconfigure.php` is system-scope
(`default-user`, `base-url`, `auth-type`, `db-*`, anonymous, api) and
`update-user.php` writes a fixed value list that excludes it. It lives in the
user's own config file, so grant it by editing that:

```sh
kubectl exec -n freshrss deploy/freshrss -c app -- \
  sed -i "s/'is_admin' => false/'is_admin' => true/" \
  /var/www/FreshRSS/data/users/<user>/config.php
```

It takes effect immediately; confirm with
`kubectl exec -n freshrss deploy/freshrss -c app -- php -f /var/www/FreshRSS/cli/user-info.php`,
which prints a `default` and an `admin` column per user. Note that deleting and
re-creating an account (including by auto-registration) resets the flag, so this
is a step to repeat rather than a one-time setup.

If you would rather grant admin through the UI, give `freshrss-admin` a real IdP
identity — an lldap user in `freshrss-admins` with a generated password, in the
style of `authelia-bind-user.json` — and log in as it. That changes nothing in
this chart; it only decides whether the placeholder is reachable. The cost is
2FA enrollment for that identity, since `freshrss-admins` is `two_factor`.

Note also that `default_user` is the account anonymous visitors would land on if
`allow_anonymous` were ever enabled — another reason it is a placeholder holding
no feeds rather than a real reader's account.

### What is reachable without an Authelia session

`mod_auth_openidc` is scoped to `<Directory /var/www/FreshRSS/p/i>`. Everything
else under the DocumentRoot (`p/`) is `Require all granted` — that is the real
security boundary, so state it precisely:

- **`/i/*` — gated.** The whole application: reading, admin, settings, the
  installer, every state-changing request.
- **`/api/greader.php`, `/api/fever.php` — open by design.** Mobile clients
  can't do an interactive OIDC flow, so they authenticate with a per-user
  **API password** instead, set in FreshRSS under *Profile → API management*.
- **`/themes/*` and the rest of `p/` — open.** Static CSS/JS/images only.

To turn the API off entirely, flip `--api-enabled` to `false` in
`FRESHRSS_INSTALL` — but note that `FRESHRSS_INSTALL` only runs on a *fresh*
data volume; on an existing install, change it in the UI instead.

## Hardening

The app is internet-facing, so:

- **Non-root, unprivileged**: uid/gid 33 (`www-data`), `runAsNonRoot`, all
  capabilities dropped, no privilege escalation, `RuntimeDefault` seccomp, no
  service account token mounted. Apache listens on 8080 because uid 33 can't
  bind 80 (`LISTEN=0.0.0.0:8080`).
- **Read-only root filesystem**, including the init container. The image's
  entrypoint wants to write `/etc/apache2` (it seds the vhost for
  `LISTEN`/`TRUSTED_PROXY`, and `a2enmod auth_openidc` symlinks into
  `mods-enabled`), `/etc/php` (php.ini timezone + upload limits) and
  `/var/lib/apache2` (Debian's per-module state markers, which `a2enmod` also
  clears — miss this one and it exits 1 even though the symlink was created).
  The `copy-etc` init container copies all three trees into an emptyDir that the
  app container mounts over the originals, which is what makes non-root +
  read-only possible at the same time. The remaining writable paths are all
  emptyDirs: `/run/apache2`, `/var/lock/apache2`, `/var/lib/php/sessions`,
  `/tmp`.
- **SSRF containment**, the main vulnerability class for a feed reader, since
  feed URLs are user-supplied. Two layers: `INTERNAL_HOST_ALLOWLIST` is left at
  its default (FreshRSS refuses private/loopback targets — *do not* set it to
  `*`), and the NetworkPolicies allow egress only to DNS, the IdP, and the
  public internet on 80/443 with private and link-local ranges excluded. Both
  the app and the refresh job carry that policy; a pod matched by none would be
  unrestricted.
- **Ingress restricted** to the ingress controller's namespace on 8080, so
  nothing in-cluster can bypass the OIDC gate by talking to the pod directly.
- `TRUSTED_PROXY` (`mod_remoteip`) limits `X-Forwarded-For` to the pod network,
  so logs and any IP-based decision see the real client and an outside client
  can't spoof one.

## Feed refresh

The image's built-in cron is bypassed: it would run `su www-data`, which needs
root. A `refresh` CronJob runs `app/actualize_script.php` unprivileged instead,
every 15 minutes, and traps nothing — a non-zero exit fails the Job, which the
standard `KubeJobFailed` / `KubeJobNotCompleted` alerts already watch.

Because it writes the same SQLite database, it mounts the same RWO volumes and
so pins itself to the app pod's node with `podAffinity`. Two consequences:

- While a refresh runs, the app pod cannot move to another node — it would block
  on the volumes. `activeDeadlineSeconds: 600` bounds that.
- If the app pod isn't running, the job can't schedule and eventually fails its
  deadline. That is a true statement about feed refresh, so alerting on it is
  correct rather than noisy.

## Storage

| Volume | Contents | Backed up |
| --- | --- | --- |
| `data` | SQLite database, `config.php`, user profiles, API tokens, extension data | yes (`recurring-job-group.longhorn.io/backup`) |
| `cache` | `data/cache` (fetched HTTP responses) and `data/favicons` | no — rebuilt on demand |

Feeds and articles are the *same* per-user SQLite database, so the subscription
list cannot be backed up without the article bodies coming along. Article volume
is managed with FreshRSS's purge/archiving settings, not by moving files
between volumes.

## Secrets

Two, both generated in-cluster by mittwald secret-generator — nothing is
committed:

| Secret | Key | Purpose |
| --- | --- | --- |
| `freshrss-oidc-authelia` | `client-secret` | The OIDC client secret, shared with the IdP. |
| `freshrss-oidc-crypto` | `crypto-key` | `mod_auth_openidc`'s `OIDCCryptoPassphrase`, for its own session encryption. The IdP never sees it, hence a separate, non-reflectable Secret. |

Both are in `ignoreDifferences` in `application.yaml` so ArgoCD doesn't fight
the generator.

## First deploy

On first start the entrypoint runs `cli/do-install.php` (writes `config.php`)
and then `cli/create-user.php` (creates the `freshrss-admin` placeholder —
`do-install` alone only records who the default user *will be*). Both exit 3 =
"already exists" on every restart afterwards and the entrypoint treats that as a
no-op, so there is never a setup wizard exposed on a public hostname.

Then log in, which auto-registers your own account, and grant it `is_admin` as
described above.

Expect one alarming-looking line in the first-start logs: `actualize-user.php
failed!`. The entrypoint refreshes every user after creating one, and that
script ends in `done($nbUpdatedFeeds > 0)` — so it "fails" simply because the
placeholder has no feeds. Its exit status is ignored, and the refresh CronJob
uses `actualize_script.php`, which does not behave that way.

`--default-user`, `--base-url` and `--language` only take effect on that first
run. To change them later, use `cli/reconfigure.php` in the pod rather than
editing `FRESHRSS_INSTALL` and expecting it to reapply.
