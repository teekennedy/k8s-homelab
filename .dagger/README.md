# Dagger modules

CI for this repo is a Dagger 1.0 workspace: one module per language or
platform, installed in the root `dagger.toml`. `dagger check` runs every
module's checks, and fails on any generator that would change a file;
`dagger generate -y` applies the generators.

```bash
dagger check                            # everything
dagger check -l                         # list checks; --all lists every item
dagger check --go                       # one module
dagger check --go-module=cmd/lab        # one item of a collection
dagger check --helmfile --helmfile-release=traefik
dagger check nix:validate               # one check outside a collection

dagger generate -y                      # apply every generator
dagger generate --go -y                 # one module's generators

dagger api functions                    # list modules
dagger api call go --help               # a module's functions
dagger api call kubernetes integration-test --releases=reflector
dagger shell devenv/toolchain           # a shell in the container checks run in
```

A module's functions are named for what they do (`test`, `lint`, `format`)
without repeating the language, since the module name comes first.

## Layout

```
dagger.toml                 # the workspace: installed modules and their settings
dagger.lock                 # pinned versions of remote modules and the Go SDK
.dagger/
  lib/                      # plain Go shared by modules (no Dagger types)
    cachetest/              # the cache-granularity test harness
    daggerfake/             # an in-process model of the engine, for that harness
    cacheprobe/             # lets a test binary import a generated SDK
    helmfile/               # helmfile state parsing for helmfile and kubernetes
  modules/<name>/           # one Dagger module (Go SDK) per language or platform
    dagger-module.toml
```

Official modules can run in our toolchain too, through the same `base`-style
wiring. We tried [`dagger.io/python/uv`](https://github.com/dagger/python) for
its lock generator, but it rediscovers every project and rebuilds its
containers on every run, uncached, which made a `dagger check` with nothing to
do take about 25 seconds longer. Our python module's `lock` generator does the
same job per project, cached like everything else.

## Modules

| Module | Collections | Checks | Generators | Other functions |
| --- | --- | --- | --- | --- |
| `ci` | | `verify-cache-granularity` | | |
| `cue` | | | `export`, `fix`, `format`, `trim` | |
| `devenv` | | | | `toolchain`, `integration-toolchain`, `container`, `projects`, `update` |
| `go` | `modules` | per module: `test` | per module: `lint` | |
| `helmfile` | `environments`, each with `releases` | per environment: `lint`; per release: `build`, `kubeconform`, `polaris`, `validate` | | |
| `kubernetes` | | | | `integration-test`, `integration-reports`, `dev-cluster`, `dev-cluster-delete` |
| `lab` | | `build` | `update-vendor-hash` | `binary`, `binary-nix` |
| `nix` | | `validate`, `validate-build-metrics` | `format` | `image` |
| `python` | `projects` | per project: `test` | per project: `format`, `lock` | |
| `release` | | `build-dockerfiles`, `test-scripts` | | `image`, `publish` |
| `terraform` | `modules` | `lint`; per module: `validate` | `format`; per module: `lock` | |
| `woodpecker` | | `lint` | | |
| `yaml` | | `lint` | | |

Each module's package doc says what it covers, and `dagger api call <module>
<function> --help` documents its arguments. The two with operating manuals of
their own are [kubernetes](modules/kubernetes/README.md) (integration tests and
dev clusters) and [release](modules/release/README.md) (container releases).

## How a module is put together

### The toolchain container

Every tool a check runs comes from devenv.lock, so a check runs the same
alejandra, black or golangci-lint as a local `devenv shell`. The `devenv`
module builds that shell as a container, and `dagger.toml` wires it into every
other module's constructor:

```toml
[modules.go.settings]
container = "dag://devenv/toolchain"
```

```go
// New is what `dagger api functions` shows as the module's description.
func New(
	// Container to run in. dagger.toml wires in devenv's toolchain.
	container *dagger.Container,
) *Go {
	return &Go{Container: container}
}
```

The modules don't depend on devenv; the workspace connects them. `kubernetes` gets
`dag://devenv/integration-toolchain` instead, which adds kubectl and curl, and
`lab` also gets `dag://nix/image` for its Nix builds. Only `release` still
depends on devenv directly, because it builds the published images with the
profiles each release tag names. The cache tests pass the constructor a cheap
stand-in instead of a Nix build.

The devenv module's own `+ignore` decides what invalidates the toolchain:
`devenv.nix`, `devenv.yaml`, `devenv.lock` and `cmd/lab/devenv.nix`, which
devenv.yaml imports. Nothing a check reads can reach it. The lab CLI itself is
only installed by the `interactive` profile, so editing its Go source leaves
the toolchain cached; `devenv container`, which builds the published images and
may include that profile, reads all of `cmd/lab` from a source of its own.

The exceptions build with plain Nix rather than devenv: `nix:validate`,
`nix:validate-build-metrics`'s probe, and `lab:build`, `lab binary-nix` and
`lab:update-vendor-hash`.

### Collections

A module that works on many units — Go modules, Python projects, Terraform
modules, helmfile releases — returns them as a collection: a struct marked
`+collection` whose `+keys` field lists the units, with a `+get` method that
returns one. Checks and generators are methods on the unit, so Dagger runs one
per unit, lists each in `dagger check -l --all`, and adds a selector flag per
collection (`--go-module`, `--python-project`, `--terraform-module`,
`--helmfile-environment`, `--helmfile-release`).

Helmfile's environments come from the `environments` setting (by default
`["production"]`), and each environment holds the releases of every chart.

### Source and cache scoping

Every function that reads the repo takes it as a `+defaultPath="/"` directory
filtered by `+ignore` down to the files it reads, so the collection is only
rebuilt when one of those changes. Each unit then carries only its own slice of
that directory, so each unit's check caches on its own files:

```
dagger check --go
├─ go-module=cmd/lab                     → cached (unchanged)
├─ go-module=k8s/.../secret-sync          → re-runs (file changed)
└─ go-module=.dagger/modules/go (+ ../../lib, its local replace target)
```

A changeset built from a scoped directory has paths relative to that scope, so
generators graft both sides back under the unit's repo path before diffing;
otherwise `dagger generate -y` would write to the repo root.

| Unit | Scope |
| --- | --- |
| Go module | Its directory, plus the targets of any local `replace` in its go.mod |
| Python project | Its directory, plus `k8s/testlib` for a release's integration tests |
| helmfile release | `helmfile.yaml.gotmpl`, one environment's `env.json`, its chart and `k8s/charts` |
| Terraform module | All of `terraform/`: modules reference siblings by relative path |

All modules set `legacy-default-path = true`, which keeps 0.x `+defaultPath`
semantics. Moving to 1.0's Workspace API instead would let modules find units
and load files lazily, but the scoping above, and the tests that hold it, rest
on the current loading.

### Checks and generators

A `+check` returns only an error. A formatter is a `+generate` function
returning a `*dagger.Changeset`: `dagger check` runs it as a staleness check
that fails when the changeset is non-empty, and `dagger generate -y` applies
it. When one generator runs several tools, the destructive one runs first
(deadnix before alejandra).

The `scripts/tests` project's `python` test is a deliberate no-op:
`release:test-scripts` runs it, with the scripts and manifests it reads
mounted beside it.

A check's body runs again on every `dagger check`; what makes a warm run fast
is that its execs are served from cache. A failed exec is never cached, so an
exec whose non-zero exit a check treats as a pass (pytest's 5, "no tests
collected", for a project that deselects all of its tests) has to turn it into
success inside the exec, or it re-runs every time.

`devenv update` is deliberately not a generator: it fetches whatever is latest,
so it is not reproducible. Renovate runs it weekly with
`dagger api call devenv update -y`, alongside `dagger lock update`.

### Generated code is committed

Each module's `dagger.gen.go` and `internal/dagger` are checked in, and
`codegen.automaticGitignore` is off. In 1.0 Dagger loads a module from its
committed bindings, and `go:test` and `go:lint` compile these modules like any
other Go module. SDK codegen is the `dagger-go-sdk` generator, so
`dagger generate -y` keeps them current. Its staleness check is skipped in
`dagger check` (`check.skip` in dagger.toml), because it regenerates every
module's bindings on every run, uncached, which tripled the time of a run with
nothing to do; CI runs the generator after `dagger check` and fails on a diff.

After changing a module's API, the committed `dagger.gen.go` no longer
compiles, and Dagger cannot load the module to list it. The generator
regenerates it anyway: run `dagger generate --dagger-go-sdk -y` and ignore the
load errors it reports for the stale modules, then run it again to confirm.
When a module stops depending on another, delete the dependency's
`internal/dagger/<dep>.gen.go` and `internal/dagger/core/<dep>.gen.go`; the
generator leaves them behind.

Keep `engineVersion` in every `dagger-module.toml` at the engine's version
(`k8s/platform/dagger-engine/values.yaml`), which is also the CLI that CI and
Renovate install.

## The shared library

`.dagger/lib` is a plain Go module, `dagger/lib`. A module that uses it lists
`../../lib` under `include` in its `dagger-module.toml`, so the engine uploads
it with the module, and has `replace dagger/lib => ../../lib` in its go.mod.
Code in it cannot use Dagger types: each module generates its own SDK package,
so a `*dagger.Directory` from one is a different type from another's. That is
why `lib/helmfile` only parses, and the helmfile and kubernetes modules each
keep their own few lines of Dagger calls around it.

Only the modules that need it include it (go, helmfile, kubernetes, release),
because a change to it changes the source of every module that does.

## Cache-granularity tests

`go`, `helmfile` and `kubernetes` assert that a source edit re-runs exactly the
units it should — the property the scoping above exists for — with the harness
in `lib/cachetest`. It runs each check over every unit twice, before and after
an edit, and compares per-unit exec cache keys:

- **Fake backend** (`go:test`, milliseconds): `lib/daggerfake` evaluates the
  check's GraphQL against a content-addressed model of the engine and records a
  cache key per exec, without running anything.
- **Engine backend** (`ci:verify-cache-granularity`): the same scenarios against
  a real engine, with the check's tools replaced by shims that count their runs.
  Without a Dagger session these tests skip.

A module adds coverage with a fixture, a scenario table and a
`cachetest.Check`, plus a `client` adapting the harness to its generated SDK
(copy `modules/go/client_test.go`). See the `cachetest` package doc.

## Adding a module

1. `dagger module init go --name <name> -y`, which creates
   `.dagger/modules/<name>` and installs it. Set
   `codegen.automaticGitignore = false` in its `dagger-module.toml`, and add
   `legacy-default-path = true` to its `[modules.<name>]` entry in
   `dagger.toml`.
2. Write `main.go` with the constructor above, and wire its container:

   ```toml
   [modules.<name>.settings]
   container = "dag://devenv/toolchain"
   ```

3. `dagger generate --dagger-go-sdk -y`.
4. `dagger check --<name>`, then commit the generated code with it.

## Publishing to the Daggerverse

Not done, on purpose. A module in a monorepo is versioned by tagging with its
subpath as a prefix (`<subpath>/v1.2.3`) — which a module under `.dagger/`
cannot have, because git rejects a ref component that starts with a dot. These
could only ever be referenced by branch or commit; a module meant for
publishing would live outside `.dagger/`, as the Dagger repo's own published
ones do under `modules/`.

What would make one worth publishing is not true of any of these yet:

- They hardcode this repo's layout (`k8s/<tier>/<chart>`, `config/gen`,
  `cmd/lab`, `.golangci.yaml` at the root) rather than discovering it.
- They run in the devenv toolchain, which only exists for this repo.

The best candidates are `lib/daggerfake` and `lib/cachetest`, which are generic,
but they are Go test libraries rather than Dagger modules: publishing them means
a `go get`-able Go module.

## Known limitations

- `kubernetes` needs an engine that allows privileged execs, so nothing in it is
  a check. See its README.
