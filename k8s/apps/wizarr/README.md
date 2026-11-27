# Wizarr

[Wizarr](https://docs.wizarr.dev) hands out invitation links that create
Jellyfin accounts and walk new users through setting up a client. It is public
at `https://invites.msng.to`.

## Authentication

Wizarr has no OIDC client of its own. Its SSO mode is `DISABLE_BUILTIN_AUTH`,
which turns `/login` into "sign in whoever asks as admin" and expects a proxy
in front. Here that proxy is a dedicated oauth2-proxy (`wizarr-auth`), which
owns the HTTPRoute and signs admins in through Authelia (client `wizarr`,
LLDAP groups `wizarr-admins` and `full-admin`, always two-factor).

Invitees have no account yet, so the invite pages (`/j/…`, `/join`,
`/wizard`, …) skip SSO through oauth2-proxy's `skip_auth_routes`. The list is
Wizarr's documented Authelia bypass list minus `/setup`, which creates the
first admin account without any auth. `/login` must never be on it.

A NetworkPolicy admits only the oauth2-proxy pod to Wizarr, since any other
caller could reach `/login` directly.

## First run

1. Add yourself to `wizarr-admins` (or `full-admin`) in LLDAP.
2. Open `https://invites.msng.to`, sign in through Authelia, and create the
   admin account `/setup` asks for. Built-in login is off, so its password is
   never used, but Wizarr refuses to serve anything else until one exists.
3. Add the Jellyfin server under Settings → Media servers. Use
   `http://jellyfin-main.jellyfin.svc.cluster.local:8096` as the server URL
   and `https://jellyfin.msng.to` as the external URL shown to invitees.

All settings, including the Jellyfin API key, live in the SQLite database on
the `wizarr-data` volume, not in this repo.
