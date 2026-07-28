# copyparty

Web file manager at **https://files.msng.to** — the browse / upload / download /
share half of the Dropbox replacement. [syncthing](../syncthing) already handles
device-to-device sync; copyparty adds the web UI and share links on top of the
same NAS.

> The upstream doc lives at <https://github.com/9001/copyparty#readme> and the
> full option list at <https://copyparty.eu/helptext.txt>.

## Shape

| Piece | Where |
| --- | --- |
| copyparty | Deployment, 1 replica, `Recreate` (single writer for the index + dedup symlinks) |
| Config | ConfigMap → `/cfg/copyparty.conf` (see `files/copyparty.conf`) |
| User data | static NFS PV over mTLS → `/w`, mounted as the `[/f]` volume |
| Index / thumbs / shares.db / idp.db | Longhorn RWO PVC → `/state` |
| SSO | dedicated `oauth2-proxy` subchart + two Traefik `Middleware`s |
| Exposure | three Ingresses on the `wspublic` entrypoint |

## Auth

copyparty has **no local accounts**. Identity arrives as headers, asserted by a
dedicated oauth2-proxy running as a Traefik `forwardAuth` middleware — but only
for paths in `.Values.protectedPaths` (currently just `/f`). Everything else,
including `/`, never runs forwardAuth at all:

```
browser ──► Traefik (wspublic)
              ├─ /f  → signin(errors 401 → /oauth2/sign_in)
              │        forwardAuth ──► copyparty-auth (oauth2-proxy) ──► Authelia
              │        ──────────────► copyparty :3923  (X-Auth-Request-* headers)
              ├─ /oauth2 → copyparty-auth (the auth flow itself)
              └─ /  (everything else) ─► copyparty :3923  (no headers, ever)
```

`forwardAuth`, rather than oauth2-proxy's reverse-proxy `upstreams` mode,
keeps request **bodies** off the proxy — only the small auth subrequest goes
there. Makes a difference with large uploads.

Why `/` isn't gated: copyparty's own JS treats the site root as an API surface
(`?ls`, `?setck`, ...) regardless of which volume the user is actually
browsing. Gating root meant every one of those calls hit a cross-origin
redirect into Authelia that the browser refused to follow (CORS), leaving the
UI spinning forever. copyparty.conf's `[/]` volume has no ACL entries at all,
so an unauthenticated request that lands here — which, since headers never
arrive, is *every* request that lands here — gets a same-origin "access
denied" instead. The only sanctioned way for an anonymous visitor to read a
file is a `/share` link.

### Groups

Bootstrapped in lldap (`k8s/platform/auth-system/files/lldap-bootstrap/group-configs/service-groups.json`),
released by Authelia in the `groups` claim, enforced twice — by oauth2-proxy's
`allowed_groups` (can you get in at all) and by copyparty's per-volume ACLs (what
can you do):

| Group | `/f` (`/w`) | `/` |
| --- | --- | --- |
| _(anonymous)_ | — | — |
| `copyparty-users` | `rwmd.` | — |
| `copyparty-admins` | `A` (= `rwmda.`) | — |
| `full-admin` | `A` | — |

`/` is unreachable by every group, including admins — see "Why `/` isn't
gated" above.

`A` adds admin: uploader IPs, upload timestamps, and the control panel's
`[reload cfg]` button (which re-reads volumes without a restart; `[global]`
changes still need the pod to roll, which the config checksum annotation does).

### Anti-spoofing

Three independent controls, which is why `--idp-h-key` is *not* set (its shared
secret would have to be committed in cleartext to both the Middleware and the
ConfigMap, buying nothing over these):

1. Traefik **deletes** every header named in `authResponseHeaders` from the
   inbound request before copying the auth response's values in — a client
   cannot smuggle its own `X-Auth-Request-User`.
2. copyparty's `xff-src: 10.42.0.0/16` means the IdP headers (and the real-ip
   header) are honoured **only** for connections from the pod network. Verified
   against the live cluster: Traefik's pods are `10.42.{0,2,3}.x`.
3. A `NetworkPolicy` restricts `:3923` to the Traefik namespace, so nothing else
   in-cluster can reach copyparty to try in the first place.

⚠️ `xff-src` is the sharp edge: if it ever stops covering Traefik's pod IP,
copyparty silently ignores the identity headers and **every user becomes
anonymous**. The symptom looks like "SSO broke", not like a misconfigured CIDR.

### Protected paths

Nothing on the host requires a session except the prefixes in
`.Values.protectedPaths` (currently just `/f`) — the inverse of the old model,
where everything was gated except an allowlist. This is defence-in-depth only
in the sense that copyparty applies its own ACLs regardless: no volume has an
`r: *` entry anymore, so a request that reaches copyparty without identity
headers — which includes every request outside `/f` — can't read anything
except through a `/share` link (upstream's README warns about exactly this
kind of bypassed-prefix assumption, which is why the ACLs are the real gate,
not the Ingress split).

A volume added later and forgotten from `protectedPaths` is exposed by
default, not protected — keep the list explicit, and keep the copyparty ACL as
the backstop.

Traefik ranks routers by rule specificity, so the `/f` and `/oauth2` routers
beat the catch-all `/` router without any explicit priority.

## HEIC / HEIF thumbnails — not supported (deferred)

**No** official copyparty image can thumbnail HEIC/HEIF or HEVC video. Upstream
strips those codecs for patent reasons: `copyparty/iv` installs libvips but
deliberately omits Alpine's `vips-heif`, and its FFmpeg is built by
`scripts/docker/base/build-no265.sh`. No well-maintained downstream image adds
them back.

The fix is a one-line derived image, which needs a build + registry + renovate
entry, so it's deferred as agreed:

```dockerfile
FROM copyparty/iv:1.20.19
RUN apk add --no-cache vips-heif
```

Then set `image.repository` to the derived image. `--th-dec` already prefers
`vips` after `pil`/`ff`, so no config change is needed. copyparty also probes for
`pillow-heif` at startup (it shows up in the `optional-dependencies` log line as
`NG:`), so `pip install pillow-heif` in the derived image is an equivalent route.

Unrelated but visible in that same log line: `iv` ships no Mutagen, so media tags
are read via FFprobe. That's a supported fallback, just slower.
