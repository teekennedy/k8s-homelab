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

`settings.json` holds every integration secret in clear text (the Radarr,
Sonarr, and Jellyfin API keys, and the OIDC client secret below), and Seerr
copies it to `settings.old.json` on start. Both are in the volume's Longhorn
backups.

Two things are declared here:

- **API key**: the `API_KEY` env var, from the generated `seerr-api-key`
  Secret, overrides the key in `settings.json`. That Secret was seeded with the
  old Seerr's key at migration, so existing clients kept working.
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
login toggle, or the application URL. On a fresh install it waits for Seerr to
create `settings.json`, so OIDC appears after the second start.

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

### X-Api-User proxy

`seerr-api-user-proxy` (`files/api-user-proxy/`) restores per-user requests for
clients that send `X-Api-User`. It rewrites those calls onto SeerrNG's own
on-behalf-of parameters, so quotas, auto-approval and permissions are the
named user's. For example, `POST /request` gets `"userId"`, and `GET /request`
is filtered to the user's own requests. Writes it doesn't know how to map are
refused (403) instead of being made as the owner. Calls without the header
pass through unchanged.

Only the admin API key may name a user, and only the `jellyfin` namespace (and
Traefik, for the withdrawals below) can reach the proxy. Its address is
`http://seerr-api-user-proxy.seerr.svc.cluster.local`.

### Withdrawn requests

Users can withdraw requests from the Requests page in the full UI, or Lumen:
their own, or anyone's if they may manage requests. SeerrNG only cancels
downloads still in progress, so whatever was already downloaded stays in
Radarr or Sonarr. The same proxy fixes that.
`templates/withdrawal-httproute.yaml` routes `DELETE /api/v1/request/*` from
the browser to it, and it forwards the call unchanged. If SeerrNG accepts the
withdrawal (204), the proxy cleans up after it (`files/api-user-proxy/withdraw.py`).
Only non-4K requests are cleaned up, and a request someone declined doesn't
count as wanting anything.

A **movie** is deleted with SeerrNG's "Delete from Library", run as the owner,
when all of these hold:

- no other request for the movie remains;
- every copy is in the one Radarr configured here, added no earlier than the
  request (so the request is what brought it in);
- if Radarr has a file, no Jellyfin user other than the requester has played
  any of it.

A **show** is trimmed to what the remaining requests still cover, if the
withdrawn request was approved. Its episodes that no other request covers are
unmonitored in Sonarr and their files deleted, except:

- episodes whose file is older than the request;
- seasons a Jellyfin user other than the requester has played any episode of
  (without the Jellyfin key below, every season with a file);
- shows where SeerrNG maps its season numbers onto different Sonarr ones.

A season left with no monitored episodes is unmonitored as well. When no
request remains, Sonarr added the show no earlier than the request, and no
season had to be kept, the whole show is deleted with "Delete from Library"
instead. A show that was in Sonarr before its requests is only ever trimmed,
so it can be left in Sonarr with nothing monitored.

Anything it can't verify is kept. Note that SeerrNG's own wording for the
button ("Delete", "permanently remove this entry and its history") says
nothing about the media, so users should know that deleting a request they no
longer want also deletes what nobody else wants.

Each withdrawal logs one line,
`withdrawn request <id> (<type>, tmdb <id>): removed|trimmed|kept, <reason>`:

```sh
kubectl -n seerr logs deploy/seerr-api-user-proxy | grep withdrawn
```

Radarr's and Sonarr's recycle bins (`/data/media/.recycle/{radarr,sonarr}`,
14 days, set by the configarr CronJob's `arr-backup-settings` container in the
jellyfin chart) keep deleted files recoverable for a while. To restore one,
re-add the title in Radarr or Sonarr without searching, move its folder back
into the library, and rescan. The NAS keeps no snapshots, so after 14 days a
deletion is permanent.

The Radarr and Sonarr keys come from `jellyfin/api-keys`, reflected as
`seerr-arr-api-keys`. The Jellyfin key for the play check has to be created by
hand, because Jellyfin keeps API keys in its own database. Until it exists,
downloaded media is kept.

1. Jellyfin → Dashboard → API Keys → **+**, name it `seerr-withdrawals`.
2. ```sh
   kubectl -n seerr create secret generic seerr-jellyfin-api-key \
     --from-literal=JELLYFIN_API_KEY=<the key>
   kubectl -n seerr rollout restart deploy/seerr-api-user-proxy
   ```

### Jellyfin Enhanced

The Jellyfin Enhanced plugin shows Seerr results in Jellyfin's search and
recommendations, and requests from inside Jellyfin. It reaches Seerr through
the X-Api-User proxy above, which files each request as the matching Seerr
user (matched on the Jellyfin user ID). The plugin is configured in the
Jellyfin dashboard, not in any chart:

- **Seerr URL**: `http://seerr-api-user-proxy.seerr.svc.cluster.local`
- **API key**: `kubectl -n seerr get secret seerr-api-key -o jsonpath='{.data.api-key}' | base64 -d`

Keep the plugin's issue reporting and watchlist sync off: the proxy refuses
those writes rather than make them as the owner.

## Rate limits

SeerrNG hardcodes its API rate limits, and some are too low for ordinary
browsing. Every title card on a page fetches that title's details, and those
lookups share a budget of 60 a minute per user, so a few pages of search
results or Discover shelves end in 429s, which the UI shows as "Movie details
could not be fetched" or "please try again later". Search allows 30 a minute.
Jellyfin Enhanced's calls all count against the owner.

`files/rate-limits.cjs`, preloaded with `NODE_OPTIONS=--require` like the OIDC
settings, multiplies every limit with a window of a minute or less by
`SEERR_RATE_LIMIT_SCALE` (10). Sign-in, password, and upload limits, which use
longer windows, are unchanged. The current limit is in each response's
`RateLimit-Policy` header.

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
