# Jellyfin

This helm chart contains the jellyfin media server, as well as some supporting services to request, download, and manage the media.

## Manual configuration steps

After deploying the helm chart, you'll need to do some manual steps to setup users and sync API keys:
- Go to prowlarr and add apps for sonarr and radarr. The API keys for sonarr and radarr are in the `api-keys` secret.
- Create your administrator account on jellyfin, then go to https://jellyfin.msng.to/web/#/dashboard/keys and create API keys for seerr, radarr, sonarr, and homepage.
  Create a secret with the value of the homepage API key by copying the key to the clipboard and running `kubectl -n jellyfin create secret generic jellyfin-homepage-api-key --from-literal=api-key="$(pbpaste)"`
  Add the other jellyfin API keys to the corresponding service.
- Qbittorrent will generate a temporary password for the admin user and log it to stdout on startup.
  Login with the temporary username / password and then reset the password. Otherwise it will be reset every time qbittorrent is restarted.
  Add the username and password to the `qbittorrent-credentials` secret.
  Assuming a username of `admin`, you can copy the password to your clipboard and use the following command to setup the secret:
  `kubectl -n jellyfin create secret generic qbittorrent-credentials --from-literal=username="admin" --from-literal=password="$(pbpaste)"`
- Create an API key named `jellyfin-exporter` on the same dashboard page and store it for the metrics exporter:

  `kubectl -n jellyfin create secret generic jellyfin-exporter-api-key --from-literal=JELLYFIN_API_KEY="$(pbpaste)"`
- Set `InactiveSessionThreshold` to `60` minutes in Jellyfin's `/data/config/config.xml` (the setting cannot be edited via the UI). It defaults to `0`,
  which disables paused-session cleanup. This could lead to 'stuck' sessions that block reboot indefinitely.

## Observability

The `jellyfin-exporter` deployment exports metrics to Prometheus. Its
ServiceMonitor and alert rules ship with this chart and can be turned on or off
under `monitoring:` in `values.yaml`. See
[files/jellyfin-exporter/README.md](files/jellyfin-exporter/README.md).

## Login: SSO and username/password both backed by LDAP

Jellyfin accepts two logins and they share one password, because both ultimately
read the same LDAP server as a single source of truth:

- **SSO** (`Community SSO for Jellyfin`): used by the web UI and anything that
  can open a browser.
- **Username / password** (`LDAP-Auth`): used by the official Jellyfin client
  apps, which do not support the SSO flow.

LLDAP is the single source of truth. A user changes their password in Authelia
and both logins follow; there is no separate Jellyfin password to keep in sync.

### What is declarative vs. manual

In git (see the commit that added this section):

- `jellyfin_bind_user` in `k8s/platform/auth-system/files/lldap-bootstrap/user-configs/`,
  in the `lldap_strict_readonly` group — Jellyfin only reads from LLDAP.
- Its generated password, `lldap-jellyfin-bind-user` in `auth-system`.
- `system-ca-bundle` (`k8s/foundation/cert-system`), a trust-manager Bundle of
  public roots plus our internal CA, mounted over the image's
  `/etc/ssl/certs/ca-certificates.crt`. LLDAP's LDAPS cert is issued by
  `internal-ca` and the LDAP-Auth plugin validates against the system store.

Not in git, because plugin config lives on the jellyfin PVC and holds the bind
password in plaintext — same as the existing SSO plugin:

**LDAP-Auth plugin** (install "LDAP Authentication" from the catalog, then
Dashboard → Plugins → LDAP-Auth):

| Setting | Value |
| --- | --- |
| LDAP Server | `lldap.auth-system.svc.cluster.local` |
| LDAP Port | `6360` |
| Secure LDAP | enabled (StartTLS off, Skip SSL Verify off) |
| LDAP Bind User | `uid=jellyfin_bind_user,ou=people,dc=msng,dc=to` |
| LDAP Bind Password | `kubectl get secret -n auth-system lldap-jellyfin-bind-user -o jsonpath='{.data.password}' \| base64 -d` |
| LDAP Base DN | `ou=people,dc=msng,dc=to` |
| LDAP Search Filter | `(&(objectClass=person)(\|(memberOf=cn=jellyfin-users,ou=groups,dc=msng,dc=to)(memberOf=cn=jellyfin-admins,ou=groups,dc=msng,dc=to)))` |
| LDAP Search Attributes | `uid, mail` — username *or* email, mirroring Authelia's `users_filter`; verified that both resolve to the same account rather than creating a second one |
| LDAP Uid Attribute | `uid` |
| **LDAP Username Attribute** | **`uid`** |
| Admin Base DN / Admin Filter | *(both empty)* |
| Allow Password Change | off |

The plugin also has an `LdapRootCaPath`. Pointing it at a mount of the existing
`cluster-ca-bundle` (internal CA only) would work just as well and would let
`system-ca-bundle` be deleted — a smaller footprint, at the cost of one more
plugin setting to remember. Either is fine; the current setup uses the trust
store so nothing plugin-side has to be configured for TLS.

Two of those are load-bearing:

- **`LDAP Username Attribute` must be `uid`.** It defaults to `cn`, which in
  LLDAP is the display name (`MsFitz`, not `msfitz`). On the wrong value the
  plugin does not match the existing Jellyfin account and creates a *second*
  one, orphaning watch history and breaking the Jellyfin Enhanced → Seerr
  `X-Api-User` match, which keys on the Jellyfin user GUID.
- **The search filter scopes login to the jellyfin groups**, so service accounts
  like `authelia_bind_user` cannot log in.

Admin/Filter are left empty deliberately: no admin rights are *granted* by LDAP.
An account that is already an administrator keeps that flag when it moves onto
the LDAP provider, so `tkennedy` is still an admin — but a new LDAP user can
never become one by editing a group in LLDAP.

`breakglass-admin` is the local account that stays behind: it is the way back in
if LLDAP is unavailable, so it must never be moved onto the LDAP provider.
`Homepage` and `jellyseerr` are service accounts and stay local for the same
reason.

**SSO plugin:** `AllowExistingAccountLink` must be **enabled**. Whichever method
a user logs in with first creates their Jellyfin account; with linking off, the
*other* method is then refused with "a pre-existing unlinked Jellyfin account
exists". It is safe here because both methods take the username from the same
LLDAP `uid` — but it does mean an LLDAP uid matching one of the remaining local
account names (`breakglass-admin`, `Homepage`, `jellyseerr`) would link to it,
so avoid creating those uids in LLDAP.

### Moving an existing user onto LDAP

`AuthenticationProviderId` is per-user and sticky: installing the plugin does
not migrate anyone. An account created before LDAP (or by SSO) stays on
`DefaultAuthenticationProvider` and will reject the user's LLDAP password with a
401 until it is flipped. There is no UI for this; it is a policy field:

```sh
# GET /Users, then POST /Users/{id}/Policy with
#   AuthenticationProviderId = Jellyfin.Plugin.LDAP_Auth.LdapAuthenticationProviderPlugin
```

If the Jellyfin username does not already equal the LLDAP uid, rename the
Jellyfin account *first* (`POST /Users/{id}` with the new `Name`) — that keeps
the account GUID and therefore the watch history. Creating a fresh account and
letting the old one rot loses both.

The rename matters for SSO too, not just LDAP: the SSO plugin finds the account
by `preferred_username`, so an admin account named `T` for LLDAP uid `tkennedy`
would have caused SSO to provision a *second* admin account instead of reusing
the existing one. `Ky` → `cleek` and `T` → `tkennedy` were both migrated for
this reason.

The plugin keeps its own link list (`LdapUsers` in its config) separate from the
policy field, but it populates that automatically on first successful login — a
flipped account does not need to be added to it by hand.
