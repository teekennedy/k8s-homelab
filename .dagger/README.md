# Dagger toolchains

CI for this repo is a set of Dagger [toolchains], one per language or platform,
listed in the root `dagger.json`. `dagger check` runs every toolchain's checks,
and fails on any generator that would change a file; `dagger generate -y`
applies the generators.

```bash
dagger check                      # everything
dagger check 'go:*'               # one toolchain
dagger check '*:lint'             # one kind of check, across toolchains
dagger check helmfile:build       # one check
dagger check -l                   # list checks and generators

dagger generate -y                # apply every generator
dagger generate go:lint -y        # apply one

dagger functions                  # list toolchains
dagger call go --help             # a toolchain's functions
dagger call kubernetes integration-test --releases=reflector
```

Checks and generators are named `<toolchain>:<function>`, and `dagger call`
takes the toolchain as its first word, so a toolchain's functions are named for
what they do (`test`, `lint`, `format`) without repeating the language.

[toolchains]: https://docs.dagger.io/core-concepts/toolchains

## Layout

```
dagger.json                 # SDK-less root: the toolchain list, nothing else
.dagger/
  lib/                      # plain Go shared by modules (no Dagger types)
    cachetest/              # the cache-granularity test harness
    daggerfake/             # an in-process model of the engine, for that harness
    cacheprobe/             # lets a test binary import a generated SDK
    helmfile/               # helmfile state parsing for helmfile and kubernetes
  modules/<toolchain>/      # one Dagger module (Go SDK) per toolchain
```

This follows the Dagger repo's own layout: an SDK-less root `dagger.json` that
lists toolchains, one Go module per toolchain, sibling modules as
`dependencies`, and plain Go shared through a `replace` plus an `include`.
Dagger has since moved its modules from `toolchains/` to `.dagger/modules/`,
which is where these live.

## Toolchains

| Toolchain | Checks | Generators | Other functions |
| --- | --- | --- | --- |
| `ci` | `verify-cache-granularity` | | `debug-dir` |
| `cue` | | `export`, `fix`, `format`, `trim` | |
| `devenv` | | | `toolchain`, `container`, `projects`, `update` |
| `go` | `test` | `lint` | `modules` |
| `helmfile` | `build`, `kubeconform`, `lint`, `polaris`, `validate` | | |
| `kubernetes` | | | `integration-test`, `integration-reports`, `dev-cluster`, `dev-cluster-delete` |
| `lab` | `build` | `update-vendor-hash` | `binary`, `binary-nix` |
| `nix` | `validate`, `validate-build-metrics` | `format` | `image` |
| `python` | `test` | `format` | `projects` |
| `release` | `build-dockerfiles`, `test-scripts` | | `image`, `publish` |
| `terraform` | `lint`, `validate` | `format`, `lock` | |
| `woodpecker` | `lint` | | |
| `yaml` | `lint` | | |

Each module's package doc says what it covers, and `dagger call <toolchain>
<function> --help` documents its arguments. The two with operating manuals of
their own are [kubernetes](modules/kubernetes/README.md) (integration tests and
dev clusters) and [release](modules/release/README.md) (container releases).

## How a toolchain is put together

### The toolchain container

Every tool a check runs comes from devenv.lock, so a check runs the same
alejandra, black or golangci-lint as a local `devenv shell`. The `devenv`
toolchain builds that shell as a container — `dagger call devenv toolchain` —
and every other toolchain depends on it:

```go
// New is what `dagger functions` shows as the toolchain's description.
func New(
	// Container to run in. Defaults to the devenv ci toolchain.
	// +optional
	container *dagger.Container,
) *Go {
	return &Go{Container: container}
}

func (m *Go) toolchain() *dagger.Container {
	if m.Container != nil {
		return m.Container
	}
	return dag.Devenv().Toolchain()
}
```

The same shape as the upstream `go` module's `base` and the `pytest` module's
`container`. Passing `--container` (`dagger call go --container=... test`)
swaps the toolchain out, which is how the cache tests run checks against a
cheap stand-in instead of a Nix build.

The devenv module's own `+ignore` decides what invalidates the toolchain:
`devenv.nix`, `devenv.yaml`, `devenv.lock` and `cmd/lab` (which devenv.yaml
imports). Nothing a check passes can reach it.

The exceptions build with plain Nix rather than devenv: `nix:validate`,
`nix:validate-build-metrics`'s probe, and `lab:build`, `lab:binary-nix` and
`lab:update-vendor-hash`, which all use `dag.Nix().Image()`.

### Source and cache scoping

Every function that reads the repo takes it as a `+defaultPath="/"` directory
filtered by `+ignore` down to the files it reads, so Dagger's function cache
only re-runs it when one of those changes. Toolchains that fan out — Go modules,
Python projects, helmfile releases, Terraform modules — then mount each unit's
own slice of that directory into its own exec, so BuildKit re-runs only the
units whose files changed:

```
dagger check go:test
├─ function cache hit? → cached result
└─ miss → one exec per module, each with only its module mounted:
   ├─ cmd/lab                       → exec cache hit (unchanged)
   ├─ k8s/.../secret-sync           → re-runs (file changed)
   └─ .dagger/modules/go (+ ../../lib, its local replace target)
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

### Checks and generators

A `+check` fails only by returning an error. Returning a changed directory or a
non-empty changeset from a check passes silently, so a formatter is a
`+generate` function returning a `*dagger.Changeset` instead: `dagger check`
fails when it is non-empty and `dagger generate -y` applies it. When one
generator runs several tools, the destructive one runs first (deadnix before
alejandra).

`devenv update` is deliberately not a generator: it fetches whatever is latest,
so it is not reproducible. Renovate runs it with `dagger call devenv update -y`.

### Generated code is committed

Each module's `dagger.gen.go` and `internal/dagger` are checked in, and
`codegen.automaticGitignore` is off, as in the Dagger repo. `go:test` and
`go:lint` treat these modules like any other Go module, and a clean CI checkout
has to compile them without running `dagger develop` first. After changing a
module's API, or upgrading Dagger, regenerate:

```bash
dagger develop --recursive   # from the repo root: every toolchain and its dependencies
```

A bare `dagger develop` at the root fails, since the root has no SDK.

`dagger develop` also bumps `engineVersion` to the CLI's version; keep it in
step across the root and every module.

## The shared library

`.dagger/lib` is a plain Go module, `dagger/lib`. A module that uses it lists
`../../lib` under `include` in its `dagger.json`, so the engine uploads it with
the module, and has `replace dagger/lib => ../../lib` in its go.mod. Code in it
cannot use Dagger types: each module generates its own SDK package, so a
`*dagger.Directory` from one is a different type from another's. That is why
`lib/helmfile` only parses, and the helmfile and kubernetes modules each keep
their own few lines of Dagger calls around it.

Only the modules that need it include it (go, helmfile, kubernetes, release),
because a change to it changes the source of every module that does.

## Cache-granularity tests

`go`, `helmfile` and `kubernetes` assert that a source edit re-runs exactly the
units it should — the property the scoping above exists for — with the harness
in `lib/cachetest`. It runs each check twice, before and after an edit, and
compares per-unit exec cache keys:

- **Fake backend** (`go:test`, milliseconds): `lib/daggerfake` evaluates the
  check's GraphQL against a content-addressed model of the engine and records a
  cache key per exec, without running anything. It models a call into a
  dependency module as an opaque container keyed on the call, and the harness
  asserts that the toolchain call never changes with an edit.
- **Engine backend** (`ci:verify-cache-granularity`): the same scenarios against
  a real engine, with the check's tools replaced by shims that count their runs.
  Without a Dagger session these tests skip.

A module adds coverage with a fixture, a scenario table and a
`cachetest.Check`, plus a `client` adapting the harness to its generated SDK
(copy `modules/go/client_test.go`). See the `cachetest` package doc.

## Adding a toolchain

1. Create `.dagger/modules/<name>/dagger.json`, with `devenv` as a dependency
   if it runs in the toolchain container:

   ```json
   {
     "name": "<name>",
     "engineVersion": "v0.21.10",
     "sdk": { "source": "go" },
     "dependencies": [{ "name": "devenv", "source": "../devenv" }],
     "codegen": { "automaticGitignore": false }
   }
   ```

2. Write `main.go` with the constructor above. Its doc comment is the
   toolchain's description in `dagger functions`.
3. `cd .dagger/modules/<name> && dagger develop`.
4. Add it to `toolchains` in the root `dagger.json`.
5. `dagger check '<name>:*'`, then commit the generated code with it.

## Publishing to the Daggerverse

Not done, on purpose. The Daggerverse indexes public git repositories, and
nothing has to be submitted: the first `dagger` call that loads a module from a
public repo publishes it, so the public GitHub mirror would serve. A module in
a monorepo is versioned by tagging with its subpath as a prefix
(`<subpath>/v1.2.3`) — which a module under `.dagger/` cannot have, because git
rejects a ref component that starts with a dot. These could only ever be
referenced by branch or commit
(`github.com/teekennedy/k8s-homelab/.dagger/modules/go@main`); a module meant
for publishing would live outside `.dagger/`, as the Dagger repo's own
published ones do under `modules/`.

What would make one worth publishing is not true of any of these yet:

- They hardcode this repo's layout (`k8s/<tier>/<chart>`, `config/gen`,
  `cmd/lab`, `.golangci.yaml` at the root) rather than taking it as arguments.
- They run in the devenv toolchain, which only exists for this repo.
- Most of what they wrap already exists upstream: `github.com/dagger/go`,
  `golangci-lint`, `helm` and `pytest`. Those pull their own tool versions
  rather than devenv.lock's, which is why this repo doesn't use them: local and
  CI runs would disagree.

The best candidates are `lib/daggerfake` and `lib/cachetest`, which are generic,
but they are Go test libraries rather than Dagger modules: publishing them means
a `go get`-able Go module, not the Daggerverse.

## Known limitations

- **Editing `cmd/lab` rebuilds the toolchain.** devenv.yaml imports `./cmd/lab`
  as a devenv module, whose `default.nix` builds the CLI from `src = ./.`, and
  its `devenv.nix` adds the result to `packages` unconditionally. So the ci
  shell's closure contains a Nix build of every Go file under cmd/lab, and
  editing one re-runs every check. Fixing it means moving `packages = [lab]`
  into the interactive profile, then narrowing the devenv module's `+ignore` to
  the Nix files under cmd/lab.
- `kubernetes` needs an engine that allows privileged execs, so nothing in it is
  a check. See its README.
