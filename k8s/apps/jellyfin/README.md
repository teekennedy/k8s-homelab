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

## Login: SSO and username/password both backed by LLDAP

Jellyfin accepts two logins and they share one password, because both ultimately
read the same LLDAP:

- **SSO** (`Community SSO for Jellyfin`) via Authelia — used by the web UI and
  anything that can open a browser.
- **Username / password** (`LDAP-Auth`) via LLDAP over LDAPS — used by the
  official Jellyfin client apps, which do not support the SSO flow. Before this,
  those clients were stuck on Quick Connect.

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
| LDAP Search Attributes | `uid, mail` |
| LDAP Uid Attribute | `uid` |
| **LDAP Username Attribute** | **`uid`** |
| Admin Base DN / Admin Filter | *(both empty)* |
| Allow Password Change | off |

Two of those are load-bearing:

- **`LDAP Username Attribute` must be `uid`.** It defaults to `cn`, which in
  LLDAP is the display name (`MsFitz`, not `msfitz`). On the wrong value the
  plugin does not match the existing Jellyfin account and creates a *second*
  one, orphaning watch history and breaking the Jellyfin Enhanced → Seerr
  `X-Api-User` match, which keys on the Jellyfin user GUID.
- **The search filter scopes login to the jellyfin groups**, so service accounts
  like `authelia_bind_user` cannot log in.

Admin/Filter are left empty deliberately: no admin rights come from LDAP, so
`T` and `breakglass-admin` stay local accounts and remain usable if LLDAP is down.

**SSO plugin:** `AllowExistingAccountLink` must be **enabled**. Whichever method
a user logs in with first creates their Jellyfin account; with linking off, the
*other* method is then refused with "a pre-existing unlinked Jellyfin account
exists". It is safe here because both methods take the username from the same
LLDAP `uid` — but it does mean an LLDAP uid matching a local account name
(`T`, `breakglass-admin`, `Homepage`, `jellyseerr`) would link to it, so avoid
creating those uids in LLDAP.

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
