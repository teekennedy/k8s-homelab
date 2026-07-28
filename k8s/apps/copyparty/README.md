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
| User data | static NFS PV over mTLS → `/w` (`files/` and `pub/` subdirs) |
| Index / thumbs / shares.db / idp.db | Longhorn RWO PVC → `/state` |
| SSO | dedicated `oauth2-proxy` subchart + two Traefik `Middleware`s |
| Exposure | two Ingresses on the `wspublic` entrypoint |

## Auth

copyparty has **no local accounts**. Identity arrives as headers, asserted by a
dedicated oauth2-proxy running as a Traefik `forwardAuth` middleware:

```
browser ──► Traefik (wspublic)
              ├─ signin(errors 401 → /oauth2/sign_in)
              ├─ forwardAuth ──► copyparty-auth (oauth2-proxy) ──► Authelia
              └─ ──────────────► copyparty :3923   (X-Auth-Request-* headers)
```

forwardAuth (rather than oauth2-proxy's reverse-proxy `upstreams` mode, which is
what the syncthing instance uses) keeps request **bodies** off the proxy — only
the small auth subrequest goes there. That matters for multi-GB uploads.

### Groups

Bootstrapped in lldap (`k8s/platform/auth-system/files/lldap-bootstrap/group-configs/service-groups.json`),
released by Authelia in the `groups` claim, enforced twice — by oauth2-proxy's
`allowed_groups` (can you get in at all) and by copyparty's per-volume ACLs (what
can you do):

| Group | `/` (`/w/files`) | `/pub` (`/w/pub`) |
| --- | --- | --- |
| _(anonymous)_ | — | `r` |
| `copyparty-users` | `rwmd.` | `rwmd.` |
| `copyparty-admins` | `A` (= `rwmda.`) | `A` |
| `full-admin` | `A` | `A` |

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

### Public (unauthenticated) paths

Everything on the host requires a session except the prefixes in
`.Values.publicPaths`. This is defence-in-depth only — copyparty applies its own
ACLs to those paths too, so a bypassed prefix still can't read a protected
volume (upstream's README warns about exactly this).

| Prefix | Why |
| --- | --- |
| `/oauth2` | the sign-in flow itself (routed to oauth2-proxy) |
| `/share` | `--shr` share links; the share's own password + expiry is the gate |
| `/pub` | the volume with `r: *` |
| `/.cpr` | copyparty's static CSS/JS — share pages are unusable without it |
| `/favicon.ico`, `/robots.txt` | browser / crawler boilerplate |

Traefik ranks routers by rule specificity, so these beat the catch-all `/`
router without any explicit priority.

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

## Bootstrap

1. **Authelia client secret.** `k8s/platform/auth-system/values.yaml` carries a
   placeholder bcrypt hash for `client_id: copyparty`. After the first sync:
   ```sh
   kubectl -n copyparty get secret copyparty-oauth2-proxy-secrets \
     -o jsonpath='{.data.client-secret}' | base64 -d
   authelia crypto hash generate bcrypt --password <that-value>
   ```
   Commit the new hash. Until then, sign-in fails at the token endpoint.
4. **Post-deploy verification.** Two things that can't be settled from a
   `helm template`:
   * **Router priority.** The unauthenticated Ingress relies on Traefik ranking
     routers by rule length. Confirm:
     ```sh
     curl -so /dev/null -w '%{http_code}\n' https://files.msng.to/oauth2/start   # 302 -> authelia
     curl -so /dev/null -w '%{http_code}\n' https://files.msng.to/.cpr/ui.css    # 200
     curl -so /dev/null -w '%{http_code}\n' https://files.msng.to/               # 302 -> authelia
     ```
     If `/oauth2/start` returns a copyparty 403 instead, add explicit
     `traefik.ingress.kubernetes.io/router.priority` annotations.
   * **`rproxy: -1`.** Set on the assumption that `X-Forwarded-For` arrives with
     the client IP appended rightmost. Upload a file and check the uploader IP
     copyparty recorded (visible to `A` users). If it shows `10.42.x.x` or
     `10.69.x.x` rather than the real client address, flip `rproxy` to `1`.
     Getting this wrong misattributes ban accounting, not access control.
