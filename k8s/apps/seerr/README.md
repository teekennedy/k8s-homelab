# seerr

Media request manager for Jellyfin, at <https://seerr.msng.to>.
`jellyseerr.msng.to`, its name before the upstream rename, redirects there.

This is [SeerrNG](https://github.com/snapetech-org/seerrng), a Seerr fork,
deployed from its own chart (`seerr-chart`). It was picked for its native OIDC
sign-in. It can also manage ebooks, audiobooks, and ROMs, but none of that is
set up yet.

## Settings

Almost all of Seerr's configuration lives in `/app/config/settings.json` on the
`seerr-data` volume, not in this chart: the Jellyfin, Radarr, and Sonarr
connections, notifications, and users' permissions are all edited in the UI.
Use FQDNs (`*.jellyfin.svc.cluster.local`) for in-cluster hosts. Seerr's DNS
cache ignores the pod's search domains.

Two things are declared here:

- **API key**: the `API_KEY` env var, from the generated `seerr-api-key`
  Secret, overrides the key in `settings.json`.
- **OIDC**: see below.

## Auth

Sign-in through Authelia, using the `seerr` OIDC client in
`k8s/platform/auth-system/values.yaml`:

| group | Policy |
| --- | --- |
| `seerr-users` | `one_factor` |
| `seerr-admins` | `two_factor` |
| `full-admin` | `two_factor` |

The groups only gate the Authelia login; they don't set Seerr permissions,
which are still managed in Seerr. Jellyfin login and local login stay enabled.

SeerrNG reads OIDC providers only from `settings.json` and has no API or UI to
edit them. The upstream chart takes no init containers either, so
`files/oidc-settings.mjs` is preloaded into the server process with
`NODE_OPTIONS=--import` and writes the provider from `SEERR_OIDC_PROVIDER` (in
`values.yaml`) into `settings.json` before Seerr reads it. That runs on every
start, so the chart always wins over hand edits to the provider, the OIDC
login toggle, or the application URL.

`newUserLogin` is off. An account created through OIDC would be a new local
account with no link to the user's Jellyfin account. Existing users link
Authelia from **Profile → Settings → Linked Accounts** after signing in with
Jellyfin.

### API key

SeerrNG binds the admin API key to the owner account. Unlike Seerr, it ignores
the `X-Api-User` header, so a client holding the key, such as the Jellyfin
Enhanced plugin, acts as the owner for every request. Per-user tokens, limited
to discovery and requests and expiring after at most 90 days, can be created
under **Profile → Settings → Client Tokens**.

## Storage

| Volume | Contents | Backed up |
| --- | --- | --- |
| `seerr-data` | `settings.json` and the SQLite database | yes (`recurring-job-group.longhorn.io/backup`) |
| `seerr-cache` | Image cache | no (`ephemeral`), refetched on demand |
| `logs` (emptyDir) | File logs, which can't be turned off | no; the same logs go to stdout |

`anime-list.xml`, refreshed daily, sits in the root of `seerr-data` and is
backed up with it.

## Migration from Seerr

This replaced the Seerr deployment in the jellyfin chart (Nov 2026), using
SeerrNG's `/app/deploy/seerrng-migrate.mjs`, which ships in the image. The
script copies `settings.json` and the database into a freshly initialized
SeerrNG and can also migrate back to Seerr. See [its guide](https://github.com/snapetech-org/seerrng/blob/main/docs/using-seerr/migrating-between-seerr-and-seerrng.md).
