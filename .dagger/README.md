# Dagger

CI for this repo is a Dagger 1.0 workspace holding one module, `homelab`
(`.dagger/modules/homelab`), installed in the root `dagger.toml` as the
workspace's entrypoint. Each language or platform is an object of its own in
it, and because the module is the entrypoint those objects are the top level of
every link: `dagger check python`, `dagger api call go modules`.

`dagger check` runs every check, and fails on any generator that would change a
file; `dagger generate -y` applies the generators.

```bash
dagger check                            # everything
dagger check -l                         # list checks; --all lists every item
dagger check go                         # one toolchain
dagger check go --homelab-go-module=cmd/lab              # one item of a collection
dagger check helmfile --homelab-helmfile-release=traefik
dagger check nix/validate               # one check outside a collection

dagger generate -y                      # apply every generator
dagger generate go -y                   # one toolchain's generators
dagger generate go --generator lint -y  # one generator of one toolchain

dagger api call --help                  # list toolchains
dagger api call go --help               # a toolchain's functions
dagger api call kubernetes integration-test --releases=reflector
dagger shell devenv/ci-toolchain        # a shell in the container checks run in
```

A function is named for what it does (`test`, `lint`, `format`) without
repeating the language, since the toolchain's name comes first. The selector
flags carry the module's name (`--homelab-go-module`), because Dagger prefixes
every type a module defines with the module's name.

## Layout

```
dagger.toml                 # the workspace: the module, the Go SDK
dagger.lock                 # pinned versions of remote modules and the Go SDK
.dagger/
  docs/                     # operating manuals: kubernetes.md, nix-builders.md, release.md
  modules/homelab/
    dagger-module.toml
    main.go                 # Homelab: the constructor and one accessor per toolchain
    <toolchain>.go          # go.go, python.go, helmfile.go, ...: what each one exposes
    <toolchain>_*.go        # the rest of a toolchain, and its tests
    internal/
      dagger/               # the generated SDK
      helmfile/             # helmfile state parsing
      cachetest/            # the cache-granularity test harness
      daggerfake/           # an in-process model of the engine, for that harness
      cacheprobe/           # lets a test binary import the generated SDK
```

### Why one module

Until October 2026 each toolchain was a module of its own, 13 in all. Dagger's
Go SDK regenerates a module's bindings as the `dagger-go-sdk` generator, whose
staleness check is part of `dagger check`, and it handles modules one at a
time: about 2 seconds each, codegen plus an upload of the whole repo, so 28
seconds for 13 modules on a warm run, and 48 while the other checks competed
for the engine. With one module it takes about 3 on its own, and a warm
`dagger check` with nothing to do takes about 16 seconds, that check included,
where it took 50 before (14 with `-j 8`, which queues checks instead of
starting all of them at once).

Merging cost nothing in caching. An exec's cache key does not include the
module that ran it, so editing one toolchain's Go source re-runs that module's
functions — cheap — while every exec they issue comes from cache unless its
own inputs changed.

Only `package main` can define what the module exposes: the codegen rejects a
type from another package ("cannot code-generate for foreign type"). So every
object, collection and function Dagger sees lives in `package main`, one file
per toolchain, and code that needs no exported Dagger type can move to
`internal/`. Since there is one generated SDK, those packages can use Dagger
types too, through `dagger.Connect()`.

## Toolchains

| Toolchain | Collections | Checks | Generators | Other functions |
| --- | --- | --- | --- | --- |
| `ci` | | `integration-test` | | |
| `cue` | | | `export`, `fix`, `format`, `trim` | |
| `devenv` | | | | `ci-toolchain`, `toolchain`, `integration-toolchain`, `container`, `projects`, `update` |
| `go` | `modules` | per module: `test` | per module: `lint` | |
| `helmfile` | `environments`, each with `releases` | per environment: `lint`; per release: `build`, `kubeconform`, `polaris`, `validate` | | |
| `kubernetes` | | | | `integration-test`, `integration-reports`, `dev-cluster`, `dev-cluster-delete` |
| `lab` | | `build` | `update-vendor-hash` | `binary`, `binary-nix` |
| `nix` | | `validate`, `validate-build-metrics` | `format` | `image` |
| `nix-builders` | | | | `status` |
| `python` | `projects` | per project: `test` | per project: `format`, `lock` | |
| `release` | | `build-dockerfiles`, `test-scripts` | | `image`, `publish` |
| `terraform` | `modules` | `lint`; per module: `validate` | `format`; per module: `lock` | |
| `woodpecker` | | `lint` | | |
| `yaml` | | `lint` | | |

`dagger api call <toolchain> <function> --help` documents each function's
arguments. Three have operating manuals of their own:
[kubernetes](docs/kubernetes.md) (integration tests and dev clusters),
[nix-builders](docs/nix-builders.md) (remote Nix builders) and
[release](docs/release.md) (container releases).

## How the module is put together

### The toolchain container

Every tool a check runs comes from devenv.lock, so a check runs the same
alejandra, black or golangci-lint as a local `devenv shell`. The `Homelab`
constructor loads the files the devenv shell is built from, and each accessor
(`Homelab.Go`, `Homelab.Python`, ...) hands its toolchain the `ci` profile's
shell as a container:

```go
// Go tests and lints every Go module in the repo.
func (h *Homelab) Go(ctx context.Context) *Go {
	return &Go{Container: h.Toolchains.CiToolchain(ctx)}
}
```

`kubernetes` gets the `ci` and `integration` profiles instead, which add kubectl
and curl, and `lab` and `nix` also get the remote Nix builders, if any are
configured, for their Nix builds. The cache
tests construct a toolchain directly, with a cheap stand-in for the container
instead of a Nix build.

What invalidates the toolchain is the constructor's `toolchainSource`:
`devenv.nix`, `devenv.yaml`, `devenv.lock` and `cmd/lab/devenv.nix`, which
devenv.yaml imports. Nothing a check reads can reach it. The lab CLI itself is
only installed by the `interactive` profile, so editing its Go source leaves the
toolchain cached; `devenv container`, which builds the published images and may
include that profile, reads all of `cmd/lab` from `devenvSource`.

The exceptions build with plain Nix rather than devenv: `nix/validate`,
`nix/validate-build-metrics`'s probe, and `lab/build`, `lab binary-nix` and
`lab/update-vendor-hash`. All but the probe, which needs a cold store, keep
their store and Nix's fetcher cache in cache volumes, and can hand their builds
to [remote builders](docs/nix-builders.md), as can the devenv builds.

### The published devenv-ci image

Building the toolchain is a Nix build. The `devenv-ci-<version>` release
publishes the same `ci` profile, built from the same four files, as
`ghcr.io/teekennedy/k8s-homelab:devenv-ci-<version>`, so `devenv ci-toolchain`
pulls that image instead of building whenever it is what a build would produce.

`modules/homelab/devenv-ci.json` records the latest release: its tag, its
image pinned by digest, its platform, and the sha256 of each of the four files
it was built from. The module embeds it, and the toolchain:

1. Compares the four files' sha256 with the record. Comparing contents also
   catches uncommitted edits, and ignores a change that was later reverted.
2. Checks that the image is for the engine's platform. Releases are built in
   CI and are `linux/amd64` only, so an Apple Silicon engine always builds.
3. Pulls the image by digest, which needs no registry lookup once the engine
   has it.

Any changed file, or a platform mismatch, builds as before, with a line in the
log saying why. A step that fails (a registry outage) builds too, with a
`warning:`. Nothing here reads Git or the network, so the decision is cached
like any other call, and PR pipelines clone shallow.

The release pipeline (`.woodpecker/release.yaml`) writes the record with
`dagger api call devenv ci-release --tag=<tag>` after publishing, and
`scripts/record-devenv-ci.sh` commits it to main as release-bot. Between a
toolchain change landing and its release being recorded, checks build the
toolchain. To record a release by hand, run the same call from a checkout of
its tag and export the file over `modules/homelab/devenv-ci.json`.

What the image does not track is the devenv CLI image `devenvShell` builds
with: a Renovate bump of it does not change the four files, so the toolchain
stays the previous release's until the next devenv release, exactly as the
published image does.

### Collections

A toolchain that works on many units — Go modules, Python projects, Terraform
modules, helmfile releases — returns them as a collection: a struct marked
`+collection` whose `+keys` field lists the units, with a `+get` method that
returns one. Checks and generators are methods on the unit, so Dagger runs one
per unit, lists each in `dagger check -l --all`, and adds a selector flag per
collection (`--homelab-go-module`, `--homelab-python-project`,
`--homelab-terraform-module`, `--homelab-helmfile-environment`,
`--homelab-helmfile-release`). A collection's types are prefixed with their
toolchain (`GoModules`, `PythonProjects`, `HelmfileReleases`) so that two
toolchains' units cannot clash in the one package.

Each unit is a check of its own in the output, `dag://?check=python/projects/test&homelab-python-project=k8s/testlib`,
and each takes as long there as the run it waited in: a hundred checks start at
once and share the engine. That is a display of wall time, not of work, and it
is what lets one unit's result cache independently of the others'.

Helmfile's environments come from the `helmfile` accessor's `environments`
argument (by default `["production"]`), and each environment holds the releases
of every chart.

### Source and cache scoping

Every function that reads the repo takes it as a `+defaultPath="/"` directory
filtered by `+ignore` down to the files it reads, so the collection is only
rebuilt when one of those changes. Each unit then carries only its own slice of
that directory, so each unit's check caches on its own files:

```
dagger check go
├─ homelab-go-module=cmd/lab                    → cached (unchanged)
├─ homelab-go-module=k8s/.../secret-sync         → re-runs (file changed)
└─ homelab-go-module=.dagger/modules/homelab     → cached (unchanged)
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

The module sets `legacy-default-path = true`, which keeps 0.x `+defaultPath`
semantics. In 1.0 a `+defaultPath` argument is loaded with the same
`Workspace.directory` call the Workspace API makes, so moving to that API would
not load anything faster: a warm run spends about 6 seconds in 34 uploads, all
incremental and mostly concurrent, and most of those load the module itself.
What it would add is discovering units without loading their files first. The
scoping above, and the tests that hold it, rest on the current loading.

### Checks and generators

A `+check` returns only an error. A formatter is a `+generate` function
returning a `*dagger.Changeset`: `dagger check` runs it as a staleness check
that fails when the changeset is non-empty, and `dagger generate -y` applies
it. When one generator runs several tools, the destructive one runs first
(deadnix before alejandra).

The `scripts/tests` project's `python` test is a deliberate no-op:
`release/test-scripts` runs it, with the scripts and manifests it reads
mounted beside it.

A check's body runs again on every `dagger check`; what makes a warm run fast
is that its execs are served from cache. A failed exec is never cached, so an
exec whose non-zero exit a check treats as a pass (pytest's 5, "no tests
collected", for a project that deselects all of its tests) has to turn it into
success inside the exec, or it re-runs every time.

Cache volumes are namespaced by the module that creates them, so renaming or
merging modules starts every cache volume (Go build cache, golangci-lint, uv)
from empty. The first run after such a change is slow even though no exec's
inputs changed.

`devenv update` is deliberately not a generator: it fetches whatever is latest,
so it is not reproducible. Renovate runs it weekly with
`dagger api call devenv update -y`, alongside `dagger lock update`.

### Generated code is committed

The module's `dagger.gen.go` and `internal/dagger` are checked in, and
`codegen.automaticGitignore` is off. In 1.0 Dagger loads a module from its
committed bindings, and `go` compiles, tests and lints this module like any
other Go module. SDK codegen is the `dagger-go-sdk` generator, so
`dagger generate -y` keeps it current, and `dagger check` fails when it is
stale.

After changing the module's API, the committed `dagger.gen.go` no longer
compiles, and Dagger cannot load the module to list it. The generator
regenerates it anyway: run `dagger generate --dagger-go-sdk -y` and ignore the
load errors it reports, then run it again to confirm. Doc comments are part of
the bindings, so editing one makes them stale too.

Keep `engineVersion` in `dagger-module.toml` at the engine's version
(`k8s/platform/dagger-engine/values.yaml`), which is also the CLI that CI and
Renovate install.

## Cache-granularity tests

`go`, `helmfile` and `kubernetes` assert that a source edit re-runs exactly the
units it should — the property the scoping above exists for — with the harness
in `internal/cachetest`. It runs each check over every unit twice, before and
after an edit, and compares per-unit exec cache keys. The keys come from
`internal/daggerfake`, which evaluates the check's GraphQL against a
content-addressed model of the engine without running anything, so the tables
run in milliseconds as part of `go`'s test.

`ci/integration-test` holds the fake to the engine. daggerfake's conformance
tests send the same GraphQL to both and require the same answers: whether an
exec is served from cache after a given change (an edit beside a mounted
subdirectory, a changed env var, an earlier exec's output…), and what `glob`,
`entries`, `contents` and `isEmpty` return. They take seconds, and skip unless
a run opts in with `HOMELAB_ENGINE_TESTS=1` under a Dagger session
(`internal/enginetest`); the check does both, and also runs the golangci-lint
behaviour test in `go_lint_test.go`. Run it after a
Dagger upgrade, and add a case when the fake learns a new call.

A toolchain adds coverage with a fixture, a scenario table and a
`cachetest.Check`; `client_test.go` adapts the harness to the generated SDK. See
the `cachetest` package doc.

## Adding a toolchain

1. Write `<name>.go` in `.dagger/modules/homelab` with the toolchain's object,
   and give it an accessor in `main.go` like the ones there. Prefix any type
   that could clash with another toolchain's with the toolchain's name.
2. `dagger generate --dagger-go-sdk -y`.
3. `dagger check <name>`, then commit the generated code with it.

## Publishing to the Daggerverse

Not done, on purpose. A module in a monorepo is versioned by tagging with its
subpath as a prefix (`<subpath>/v1.2.3`) — which a module under `.dagger/`
cannot have, because git rejects a ref component that starts with a dot. And
this module hardcodes the repo's layout (`k8s/<tier>/<chart>`, `config/gen`,
`cmd/lab`, `.golangci.yaml` at the root) and runs in a toolchain that only
exists for this repo.

The best candidates are `internal/daggerfake` and `internal/cachetest`, which
are generic, but they are Go test libraries rather than Dagger functions:
publishing them means a `go get`-able Go module.

## Known limitations

- `kubernetes` needs an engine that allows privileged execs, so nothing in it is
  a check. See [its manual](docs/kubernetes.md).
