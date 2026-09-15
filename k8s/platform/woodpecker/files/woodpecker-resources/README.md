# woodpecker-resources

Reconciles declarative configuration into the running Woodpecker server, the
way [`forgejo-resources`](../../../forgejo/files/config) does for Forgejo. Its
input is the `woodpecker-resources` key of `k8s/platform/woodpecker/values.yaml`,
rendered into `config.yaml` beside this source in a ConfigMap and run with
`go run .` by the Job in `templates/woodpecker-resources.yaml`.

It exists mostly because of one thing that has no API behind it.

## Tokens are not an API call

A Woodpecker personal access token is a JWT signed with the user's `hash`
column. `model.User` tags that field `json:"-"`, so no endpoint ever returns
it, and there is no admin route that issues a token for another account — the
only issuer is `POST /api/user/token`, which reads the `user_sess` cookie that
Woodpecker's OAuth callback sets.

So `forgeauth.go` replays the browser round trip:

```
GET  {forgejo}/user/login                     -> _csrf
POST {forgejo}/user/login                     -> session cookie
GET  {woodpecker}/authorize                   -> redirects to Forgejo
  -> {forgejo}/login/oauth/authorize          -> grant page, first time only
POST {forgejo}/login/oauth/grant              -> redirects back with ?code=
  -> {woodpecker}/authorize?code=..&state=..  -> user_sess cookie
POST {woodpecker}/api/user/token              -> the token
```

Two consequences worth knowing:

- **Both URLs in the config must be the public ones.** Forgejo checks the
  `redirect_uri` Woodpecker sends against the one registered on the OAuth app,
  so an in-cluster Service URL fails as a redirect_uri mismatch partway through
  the chain.
- **A stored token is reused.** The JWT has no expiry and `POST /api/user/token`
  is deterministic for a given hash, so once an identity is provisioned this
  path is not exercised again unless the token stops authenticating. A Forgejo
  template change cannot break an already-working install.

`hiddenInputs` resubmits *every* hidden field of the form it finds rather than
naming `_csrf`, `client_id`, `state` and the rest one by one — which is what
lets it survive Forgejo adding one, as it has before.

## Bootstrapping

The job needs an admin token before it can do anything, and getting one is the
same OAuth round trip. That works because of one line in Woodpecker's
`server/api/login.go`:

```go
if !server.Config.Permissions.Open && !server.Config.Permissions.Admins.IsAdmin(userFromForge) {
    // registration closed
}
```

An account named in `WOODPECKER_ADMIN` is exempt from the registration check, so
the `woodpecker-resources` bot can self-register on its first login even with
`WOODPECKER_OPEN` false. Every other account is pre-created through
`POST /api/users` first, which turns *its* first login into a lookup by login
name rather than a registration.

The bot also needs `admin` on the repo at the forge — not merely write — because
`POST /api/repos` refuses to activate a repo otherwise. That is what the `CI`
team in `k8s/platform/forgejo/values.yaml` is for.

## What it reconciles

| config | what happens |
| --- | --- |
| `bootstrap` | The identity the job runs as. Must be in `WOODPECKER_ADMIN`. |
| `users[]` | Pre-created via the admin API, `admin` flag converged, token minted into `tokenSecret`. |
| `repositories[].activate` | `POST /api/repos?forge_remote_id=…`, with the forge id looked up from `GET /api/user/repos?all=true`. |
| `repositories[].settings` | `PATCH /api/repos/{id}`, minimal — only fields that actually differ. Retires the "set it once in the UI" caveats, since `WOODPECKER_DEFAULT_*` applies at activation time only. |
| `repositories[].secrets[]` | Created or converged, with the value mirrored into a Kubernetes Secret. |

Secret values are **pushed on every sync**. Woodpecker never returns a secret's
value, so there is nothing to compare against, and pushing is the only way a
rotation on the Kubernetes side reaches CI. The Kubernetes Secret is the source
of truth; `generate` mints a value into it exactly once.

## Tests

```sh
go test ./...
```

`forgeauth_test.go` stands up a Forgejo and a Woodpecker that behave the way the
real ones do at each step of the chain, including the parts that are easy to get
wrong: a bad password re-renders the login page with HTTP 200, so the POST
succeeding proves nothing; and a cookie jar carried between identities would
mint the second account's token as the first.
