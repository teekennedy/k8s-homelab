# Dagger

CI for this repo is a Dagger 1.0 workspace with one module per language or
platform, all under `.dagger/modules` and installed in the root `dagger.toml`.
A module's name is the first part of every link to it: `dagger check python`,
`dagger api call go modules`, `dagger check nix/lab/build`.

`dagger check` runs every check, and fails on any generator that would change a
file; `dagger generate -y` applies the generators.

```bash
dagger check                            # everything
dagger check -l                         # list checks; --all lists every item
dagger check go                         # one module
dagger check go --go-module=cmd/lab     # one item of a collection
dagger check helmfile --helmfile-release=traefik
dagger check nix/validate               # one check outside a collection

dagger generate -y                      # apply every generator
dagger generate go -y                   # one module's generators
dagger generate go --generator fix -y   # one generator of one module

dagger api call --help                  # list modules
dagger api call go --help               # a module's functions
dagger api call kubernetes integration-test --releases=reflector
dagger shell nix/devenv/ci-toolchain    # a shell in the container checks run in
```

`dagger check -l` prints each check by the shortest part of its path that is
unique among the checks listed beside it, so `go/modules/fix/stale` shows as
`stale` on the Go module rows. `dagger check -l -f link` prints the full paths;
either form selects the check.

## Naming

Functions are named for what they do, without repeating the module's name,
which comes first. The checks and generators share one set of verbs:

| Verb | Meaning | Examples |
| --- | --- | --- |
| `test` | Run a test suite | `go/modules/test`, `python/projects/test`, `nix/test`, `release/test` |
| `lint` | Static checks that can only fail | `helmfile/lint`, `helmfile/releases/lint`, `woodpecker/lint`, `yaml/lint` |
| `validate` | The tool's own validator, or a schema | `nix/validate`, `terraform/modules/validate`, `helmfile/releases/validate` (kubeconform) |
| `audit` | Policy and security scanners | `terraform/audit` (trivy), `helmfile/releases/audit` (Polaris) |
| `build`, `render` | Produce an artifact, or prove one can be | `nix/lab/build`, `release/build`, `helmfile/releases/render` |
| `format`, `fix`, `lock`, … | Generators: rewrite files in place | `cue/format`, `go/modules/fix`, `python/projects/lock`, `nix/lab/vendor-hash` |

A check's or generator's description is what `dagger check -l` lists beside
it, and the listing prints only the doc comment's first line. So that line is
one sentence of at most 64 characters, ending with a period and not starting
with the function's name; anything more follows a blank comment line. Each
module's `doc_test.go` enforces it with `.dagger/lib/doccheck`.

A collection's selector flag is named `<module>-<item>` after the module and
the item's type: `--go-module`, `--python-project`, `--terraform-module`,
`--helmfile-release`. Dagger always prefixes it with the module's name.

## Layout

```
dagger.toml                 # the workspace: the modules, their settings, the Go SDK
dagger.lock                 # pinned versions of remote modules and the Go SDK
.dagger/
  docs/                     # operating manuals: kubernetes.md, nix-builders.md, release.md
  lib/                      # Go code the modules share (dagger/lib); no Dagger types
    cachetest/              # the cache-granularity test harness
    daggerfake/             # an in-process model of the engine, for that harness
    cacheprobe/             # lets a test binary import the generated SDK
    enginetest/             # gates the tests that need a real engine
    doccheck/               # the description format, as a test
    helmfile/               # helmfile state parsing
  modules/<name>/
    dagger-module.toml
    main.go or <name>.go    # the module's object and constructor
    *_test.go, doc_test.go  # its tests
    internal/dagger/        # its generated SDK
```

| Module | Collections | Checks | Generators | Other functions |
| --- | --- | --- | --- | --- |
| `cue` | | | `export`, `fix`, `format`, `trim` | |
| `go` | `modules` | per module: `test` | per module: `fix` | per module: `integration-test` |
| `helmfile` | `releases` | `lint`; per release: `audit`, `lint`, `render`, `validate` | | |
| `kubernetes` | | | | `integration-test`, `integration-reports`, `dev-cluster`, `dev-cluster-delete` |
| `nix` | | `test`, `validate`; `lab/build` | `format`; `lab/vendor-hash` | `devenv ci-toolchain`, `devenv toolchain`, `devenv integration-toolchain`, `devenv container`, `devenv ci-release`, `devenv projects`, `devenv update`, `lab binary`, `lab binary-nix`, `builders status`, `image` |
| `python` | `projects` | per project: `test` | per project: `format`, `lock` | |
| `release` | | `build`, `test` | | `image`, `publish` |
| `terraform` | `modules` | `audit`; per module: `validate` | `format`; per module: `lock` | |
| `woodpecker` | | `lint` | | |
| `yaml` | | `lint` | | |

`dagger api call <module> <function> --help` documents each function's
arguments. Three have operating manuals of their own:
[kubernetes](docs/kubernetes.md) (integration tests and dev clusters),
[nix builders](docs/nix-builders.md) (remote Nix builders) and
[release](docs/release.md) (container releases).

### Why one module per language

Each module is named for what it checks, so its name leads every link and
flag, and there is no wrapper module's name in front of them. The cost is the
`dagger-go-sdk` generator, whose staleness check is part of `dagger check`: it
regenerates each module's bindings one at a time.

Measured on the in-cluster engine (v1.0.0-beta.20, October 2026), warm:

| | One `homelab` module | Ten modules |
| --- | --- | --- |
| `dagger check`, nothing to do | 13s | 36s |
| of which `dagger-go-sdk/generate/stale` | 6s | 31s |
| `dagger check go` / `python` / `woodpecker` | 4s / 6s / 3s | 5s / 5s / 3s |
| `dagger check woodpecker` after editing its Go source | 12s | 7s |
| `dagger check -l` | 3s | 3s |

Everything but the SDK generator is as fast or faster split; the generator
costs about 3 seconds per module.

An exec's cache key does not include the module that ran it, so editing one
module's Go source re-runs that module's functions, which is cheap, while every
exec they issue comes from cache unless its own inputs changed. Editing one
module leaves the others' functions cached as well.

Only `package main` can define what a module exposes: the codegen rejects a
type from another package ("cannot code-generate for foreign type"). Each
module also generates its own SDK, so its Dagger types are its own. Shared code
in `.dagger/lib` therefore holds no Dagger types (the cache harness is generic
over them), and the few small helpers every module needs, such as
`withExecOutput`, are copied into each module's `util.go`.

## How the modules are put together

### The toolchain container

Every tool a check runs comes from devenv.lock, so a check runs the same
alejandra, black or golangci-lint as a local `devenv shell`. The `nix` module
builds that shell as a container. Every other module takes the container as a
required `container` constructor argument, which `dagger.toml` wires to
`dag://nix/devenv/ci-toolchain`:

```toml
[modules.go.settings]
container = "dag://nix/devenv/ci-toolchain"
```

A setting's `dag://` address is evaluated in the workspace, so it reaches the
`nix` module with that module's own settings, the remote builders included.
`kubernetes` gets `integration-toolchain` instead, the `ci` and `integration`
profiles, which add kubectl and curl. The cache tests construct a module
directly, with a cheap stand-in for the container instead of a Nix build.

What invalidates the toolchain is the `nix` constructor's `toolchainSource`:
`devenv.nix`, `devenv.yaml`, `devenv.lock` and `cmd/lab/devenv.nix`, which
devenv.yaml imports. Nothing a check reads can reach it. The lab CLI itself is
only installed by the `interactive` profile, so editing its Go source leaves the
toolchain cached; `devenv container`, which builds the published images and may
include that profile, reads all of `cmd/lab` from `devenvSource`.

The exceptions build with plain Nix rather than devenv: `nix/validate`,
`nix/test`'s probe, and `nix/lab/build`, `nix lab binary-nix` and
`nix/lab/vendor-hash`. All but the probe, which needs a cold store, keep their
store and Nix's fetcher cache in cache volumes, and can hand their builds to
[remote builders](docs/nix-builders.md), as can the devenv builds. They all
live in the `nix` module so that they share one store and one set of builders.

`release` builds the devenv images by calling the `nix` module as a dependency
(`dag.Nix(...)`, through the client `dagger.toml` generates for it). A
dependency is constructed by its caller, not from the workspace's settings, so
`release` takes the builder settings too and passes them on; `dagger.toml`'s
`ci` environment sets both.

### The published devenv-ci image

Building the toolchain is a Nix build. The `devenv-ci-<version>` release
publishes the same `ci` profile, built from the same four files, as
`ghcr.io/teekennedy/k8s-homelab:devenv-ci-<version>`, so `nix devenv
ci-toolchain` pulls that image instead of building whenever it is what a build
would produce.

`modules/nix/devenv-ci.json` records the latest release: its tag, its image
pinned by digest, its platform, and the sha256 of each of the four files it was
built from. The module embeds it, and the toolchain:

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
`dagger api call nix devenv ci-release --tag=<tag>` after publishing, and
`scripts/record-devenv-ci.sh` commits it to main as release-bot. Between a
toolchain change landing and its release being recorded, checks build the
toolchain. To record a release by hand, run the same call from a checkout of
its tag and export the file over `modules/nix/devenv-ci.json`.

What the image does not track is the devenv CLI image `devenvShell` builds
with: a Renovate bump of it does not change the four files, so the toolchain
stays the previous release's until the next devenv release, exactly as the
published image does.

### Collections

A module that works on many units — Go modules, Python projects, Terraform
modules, helmfile releases — returns them as a collection: a struct marked
`+collection` whose `+keys` field lists the units, with a `+get` method that
returns one. Checks and generators are methods on the unit, so Dagger runs one
per unit, lists each in `dagger check -l --all`, and adds a selector flag per
collection. A unit's type names what it is (`GoModule`, `PythonProject`,
`Release`), which is what its flag is named after. A type named `Module`
clashes with the core `Module` type, so the Go and Terraform ones carry their
module's name.

Each unit is a check of its own in the output,
`dag://?check=python/projects/test&python-project=k8s/testlib`, and each takes
as long there as the run it waited in: a hundred checks start at once and share
the engine. That is a display of wall time, not of work, and it is what lets one
unit's result cache independently of the others'.

helmfile renders every chart in one environment, the `helmfile` constructor's
`environment` setting (by default `production`). The Kubernetes integration
workflow renders with `dev`.

### Source and cache scoping

Every function that reads the repo takes it as a `+defaultPath="/"` directory
filtered by `+ignore` down to the files it reads, so the collection is only
rebuilt when one of those changes. Each unit then carries only its own slice of
that directory, so each unit's check caches on its own files:

```
dagger check go
├─ go-module=cmd/lab                    → cached (unchanged)
├─ go-module=k8s/.../woodpecker-resources → re-runs (file changed)
└─ go-module=.dagger/modules/go          → cached (unchanged)
```

A changeset built from a scoped directory has paths relative to that scope, so
generators graft both sides back under the unit's repo path before diffing;
otherwise `dagger generate -y` would write to the repo root.

| Unit | Scope |
| --- | --- |
| Go module | Its directory, plus the targets of any local `replace` in its go.mod (`.dagger/lib` for the Dagger modules) |
| Python project | Its directory, plus `k8s/testlib` for a release's integration tests |
| helmfile release | `helmfile.yaml.gotmpl`, the environment's `env.json`, its chart and `k8s/charts` |
| Terraform module | All of `terraform/`: modules reference siblings by relative path |

The modules set `legacy-default-path = true`, which keeps 0.x `+defaultPath`
semantics. In 1.0 a `+defaultPath` argument is loaded with the same
`Workspace.directory` call the Workspace API makes, so moving to that API would
not load anything faster. What it would add is discovering units without
loading their files first. The scoping above, and the tests that hold it, rest
on the current loading.

### Checks and generators

A `+check` returns only an error. A formatter is a `+generate` function
returning a `*dagger.Changeset`: `dagger check` runs it as a staleness check
that fails when the changeset is non-empty, and `dagger generate -y` applies
it. When one generator runs several tools, the destructive one runs first
(deadnix before alejandra).

The `scripts/tests` project's `python` test is a deliberate no-op:
`release/test` runs it, with the scripts and manifests it reads mounted beside
it.

A check's body runs again on every `dagger check`; what makes a warm run fast
is that its execs are served from cache. A failed exec is never cached, so an
exec whose non-zero exit a check treats as a pass (pytest's 5, "no tests
collected", for a project that deselects all of its tests) has to turn it into
success inside the exec, or it re-runs every time.

Cache volumes are namespaced by the module that creates them, so renaming,
splitting or merging modules starts every cache volume (Go build cache,
golangci-lint, uv, the Nix stores) from empty. The first run after such a
change is slow even though no exec's inputs changed. The toolchain's caches
(Go, uv, helm) are attached by the `nix` module, so every module that runs in
the toolchain shares them.

`devenv update` is deliberately not a generator: it fetches whatever is latest,
so it is not reproducible. Renovate runs it weekly with
`dagger api call nix devenv update -y`, alongside `dagger lock update`.

### Generated code is committed

Each module's `dagger.gen.go` and `internal/dagger` are checked in, and
`codegen.automaticGitignore` is off. In 1.0 Dagger loads a module from its
committed bindings, and `go` compiles, tests and fixes the modules like any
other Go module. SDK codegen is the `dagger-go-sdk` generator, so
`dagger generate -y` keeps it current, and `dagger check` fails when it is
stale.

After changing a module's API, its committed `dagger.gen.go` no longer
compiles, and Dagger cannot load the module to list it. The generator
regenerates it anyway: run `dagger generate dagger-go-sdk -y` and ignore the
load errors it reports, then run it again to confirm. Doc comments are part of
the bindings, so editing one makes them stale too.

Keep `engineVersion` in each `dagger-module.toml` at the engine's version
(`k8s/platform/dagger-engine/values.yaml`), which is also the CLI that CI and
Renovate install.

## Cache-granularity tests

`go`, `helmfile` and `kubernetes` assert that a source edit re-runs exactly the
units it should — the property the scoping above exists for — with the harness
in `lib/cachetest`. It runs each check over every unit twice, before and after
an edit, and compares per-unit exec cache keys. The keys come from
`lib/daggerfake`, which evaluates the check's GraphQL against a
content-addressed model of the engine without running anything, so the tables
run in milliseconds as part of `go/modules/test`.

The `go` module's `integration-test` function holds the fake to the engine. It
is not a check. daggerfake's conformance tests send the same GraphQL to both
and require the same answers: whether an exec is served from cache after a
given change (an edit beside a mounted subdirectory, a changed env var, an
earlier exec's output…), and what `glob`, `entries`, `contents` and `isEmpty`
return. They take seconds, and skip unless a run opts in with
`HOMELAB_ENGINE_TESTS=1` under a Dagger session (`lib/enginetest`);
`integration-test` does both. Run it after a Dagger upgrade, and add a case
when the fake learns a new call:

```bash
dagger api call go modules module --path=.dagger/lib integration-test
dagger api call go modules module --path=.dagger/modules/go integration-test
```

The second runs the golangci-lint behaviour test in `go_fix_test.go`.

A module adds coverage with a fixture, a scenario table and a
`cachetest.Check`; its `client_test.go` adapts the harness to its generated
SDK. See the `cachetest` package doc.

## Adding a module

1. Create `.dagger/modules/<name>` with a `dagger-module.toml` (copy another
   module's, with `include = ["../../lib"]`), a `go.mod` with
   `replace dagger/lib => ../../lib`, the module's object and its constructor,
   and a `doc_test.go` like the others'.
2. Install it in `dagger.toml`: a `[modules.<name>]` table with
   `legacy-default-path = true`, its `container` setting if it runs in the
   toolchain, and a `[sdks.go.scopes.".dagger/modules/<name>"]` table.
3. `dagger generate dagger-go-sdk -y`.
4. `dagger check <name>`, then commit the generated code with it.

## Publishing to the Daggerverse

Not done, on purpose. A module in a monorepo is versioned by tagging with its
subpath as a prefix (`<subpath>/v1.2.3`) — which a module under `.dagger/`
cannot have, because git rejects a ref component that starts with a dot. And
these modules hardcode the repo's layout (`k8s/<tier>/<chart>`, `config/gen`,
`cmd/lab`, `.golangci.yaml` at the root) and run in a toolchain that only
exists for this repo.

The best candidates are `lib/daggerfake` and `lib/cachetest`, which are
generic, but they are Go test libraries rather than Dagger functions:
publishing them means a `go get`-able Go module.

## Known limitations

- `kubernetes` needs an engine that allows privileged execs, so nothing in it is
  a check. See [its manual](docs/kubernetes.md).
