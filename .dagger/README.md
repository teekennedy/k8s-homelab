# Homelab Dagger Module

This Dagger module provides CI/CD functionality for k8s-homelab.

## Architecture

This module is **independent of the lab CLI** to avoid circular dependencies:
- Dev shell (`devenv shell`) includes lab pre-built
- CI container (`containers.ci`) does NOT include lab
- This module builds lab from scratch as part of the pipeline

See `../docs/ci-architecture.md` for details.

### Per-Project Module Pattern

Language-specific checks are organized into **per-project module structs** that
maximize cache granularity. Each struct (GoModule, PythonProject,
TerraformModule, helmfileRelease) carries a scoped source directory containing only its project's
files. This enables two levels of caching:

- **Layer 2 (Dagger function call cache)**: `dagger check test-go` caches the
  entire TestGo result. If the filtered source (all `**/*.go` files) hasn't
  changed, the check returns instantly (~0.5s).

- **Layer 1 (BuildKit content-addressed cache)**: Within TestGo, each module's
  Test() runs against a scoped subdirectory. Unchanged modules hit the BuildKit
  exec cache while only changed modules re-run.

The combination means:
- `dagger check` re-runs the fewest checks possible for any given file change
- `dagger call <type> <method>` re-tests only the changed projects within a type

### File Organization

| File | Contents |
|---|---|
| `main.go` | Homelab struct, constructor, Nix/CUE/YAML/Woodpecker/CLI functions |
| `golang.go` | GoModule struct, per-module Test/Lint, aggregate TestGo/LintGo |
| `python.go` | PythonProject struct, per-project Test/Format, aggregate TestPython/FormatPython |
| `helm.go` | Chart discovery and path matching shared by the helmfile checks |
| `helmfile.go` | Per-release helmfile Template/Validate and the shared render, aggregate BuildHelmfile/ValidateHelmfile/LintHelmfile |
| `kubernetes.go` | Per-release Polaris/Kubeconform on the helmfile render, aggregate ValidatePolaris/ValidateKubeconform |
| `k3s.go` | k3sCluster: starting, reaching, verifying and destroying an ephemeral k3s cluster |
| `integration.go` | The Kubernetes integration workflow: plan, deploy with helmfile, wait, verify, run each release's pytest suite |
| `junit.go` | Parsing pytest's JUnit XML into a summary |
| `terraform.go` | TerraformModule struct, per-module Validate, aggregate ValidateTerraform |
| `containers.go` | Container image constants and helpers |
| `paths.go` | Path filtering utilities |

## Setup

### First Time Setup

1. **Initialize the module** (generates SDK code):
   ```bash
   dagger develop
   ```

   This creates:
   - `internal/` - Auto-generated Dagger SDK
   - `dagger.gen.go` - Type definitions
   - `querybuilder/` - Query builder code

2. **Verify setup**:
   ```bash
   # List available functions
   dagger functions

   # Should show: build-cli, go-modules, python-projects, helm-charts, etc.
   ```

### After Updating Go Files

Run `dagger develop` to regenerate SDK bindings.

### If You Get SDK Version Errors

```bash
# Clean and regenerate
rm -rf internal/ dagger.gen.go querybuilder/
dagger develop
```

## Usage

### Direct Dagger Calls

```bash
# Run all checks
dagger check

# Run checks by category
dagger check 'lint*'
dagger check 'build*'
dagger check 'test*'
dagger check 'validate*'

# Run a specific check
dagger check lint-cue
dagger check build-helmfile

# List discovered projects
dagger call go-modules                # Show all Go modules
dagger call python-projects           # Show all Python projects
dagger call terraform-modules         # Show all Terraform modules

# Auto-apply formatting fixes (use format-*/fix-* functions, not check)
dagger call format-nix --auto-apply
dagger call lint-go --auto-apply      # go mod tidy + golangci-lint run --fix
dagger call format-python --auto-apply

# Regenerate the nix vendorHash for cmd/lab after a Go dependency changes
dagger call update-go-vendor-hash --auto-apply

# Build lab CLI
dagger call build-cli --source=.        # Using Nix (production)
dagger call build-cli-go --source=.     # Using Go (faster)

# Get built binary
dagger call cli-nix --source=. export --path=./lab
```

## Available Functions

All check functions use pre-call filtering for optimal caching. Only relevant files are included.
Functions annotated with `// +check` can be run via `dagger check`.

### Toolchain injection

**Every** `+check`/`+generate` function that runs in the devenv toolchain takes
an optional `container` and falls back to `ciContainer()` when it is nil:

```go
if container == nil {
    container = m.ciContainer()
}
```

That is `FormatNix`, `LintYaml`, `ValidateWoodpecker`, `FormatCue`, `FixCue`,
`TrimCue`, `ExportCue`, `TestGo`, `LintGo`, `TestPython`, `FormatPython`,
`BuildHelmfile`, `ValidateHelmfile`, `LintHelmfile`, `ValidatePolaris`,
`ValidateKubeconform`, `ValidateTerraform`, `FormatTerraform` and
`VerifyCacheGranularity`. The exceptions are the three that build with Nix
rather than the devenv shell — `ValidateNix`, `BuildCli` and
`UpdateGoVendorHash` all use `nixContainer()`, so there is nothing to inject.

Three things this buys:

- **One toolchain build per check, not per unit.** The aggregate builds it once
  and hands the same `*dagger.Container` to every chart/module/project it fans
  out to.
- **A seam for the cache-granularity tests.** They pass a cheap stand-in so the
  scenario table runs in seconds instead of waiting on a Nix build — see
  `backend_engine_test.go`.
- **Uniformity.** Any check can be driven with a container of the caller's
  choosing, which is the prerequisite for covering it in `cache_test.go`.

It also replaced a `+private DevenvSource *dagger.Directory` field that had been
copied onto `PythonProject` and `TerraformModule` so each could
rebuild the container for itself.

`Cli` and `BuildCliGo` still build their own `ciContainer()`; they are plain
functions rather than checks, so nothing drives them in a batch.

`TestKubernetesIntegration` and `KubernetesIntegrationReports` take the same
optional `container` but fall back to `integrationContainer()` — the ci profile
plus the `integration` one, which adds kubectl and curl. Nothing in `dagger
check` runs in that container; see Kubernetes integration tests below.

### Per-Project Module Types

Each takes the toolchain `container` to run in. None of them are `+check`
functions: `dagger check` only enumerates checks on the top-level `Homelab`
object, so annotating a method on one of these types never had any effect.
Because `container` is required, these are driven by their aggregate rather than
called directly from the CLI.

#### GoModule
Discovered automatically from `go.mod` files. Each module gets a scoped source directory.
- `Test(container)` - Run `go test` for this module
- `Lint(configFile, container)` - `go mod tidy` + `golangci-lint run --fix`, returning a changeset

#### PythonProject
Discovered automatically from `pyproject.toml` files.
- `Test(container)` - Run `pytest` for this project
- `Lint(container)` - Run `black --check` for this project
- `Format(container)` - Format with `black`, returning the formatted directory

#### helmfileRelease
One chart in one helmfile environment; charts are discovered from `Chart.yaml` files under `k8s/`.
- `Template(container)` / `Validate(container)` - `helmfile template` / `helmfile lint`
- `Polaris(container)` / `Kubeconform(container)` - Audit the rendered manifests

#### TerraformModule
Discovered automatically from `.tf` files under `terraform/`.
Uses the full `terraform/` directory as source since modules can reference
siblings via relative paths (e.g., `../k8s-secret`).
- `Validate(container)` - Run `tofu init` + `tofu validate` for this module

### Top-Level Checks

Nix, Go, and cmd/lab-vendorHash formatting checks are no longer separate
`Lint*`/`Check*` functions — the `dagger` CLI now includes `+generate`
functions (see Format Functions below) directly in `dagger check`, failing
the check if the generator would produce a non-empty changeset. A dedicated
`Lint*` wrapper is only worth keeping when it does something a generator
doesn't (e.g. `LintCue` also runs `cue vet`).

#### Lint Checks
- `LintCue(source, paths)` - CUE formatting and constraint validation (`cue fmt` + `cue vet`)
  - Filters: `config/**/*.cue`
  - Fix: `dagger call format-cue --auto-apply`
- `LintYaml(source, paths)` - YAML linting
  - Filters: `**/*.yaml`, `**/*.yml`, `.yamllint.yaml`
- `LintHelmfile(source, environments)` - Helmfile state validation (`helmfile build`), plus a
  cross-check that every chart has a release, every release has a chart, and every release is
  listed in the environment's `apps`
  - Filters: `helmfile.yaml.gotmpl`, `config/gen/*/env.json`, `k8s/**/Chart.yaml`

#### Validate Checks
- `ValidateNix(source)` - Nix flake check
  - Filters: `flake.nix`, `flake.lock`, `nix/**/*`
- `ValidateHelmfile(source, environments, paths)` - `helmfile lint` per release, with the values
  helmfile deploys it with
  - Filters: `helmfile.yaml.gotmpl`, `config/gen/*/env.json`, `k8s/**/*`
- `ValidateTerraform(source, paths)` - Terraform/OpenTofu validation (delegates to TerraformModule.Validate)
  - Filters: `terraform/**/*`
- `ValidateWoodpecker(source, paths)` - Woodpecker CI pipeline validation
  - Filters: `.woodpecker/*.yaml`

#### Build Checks
- `BuildCli(source)` - Build lab CLI (using Nix)
  - Filters: `cmd/lab/**/*`
- `BuildHelmfile(source, environments, paths)` - `helmfile template` per release
  - Filters: `helmfile.yaml.gotmpl`, `config/gen/*/env.json`, `k8s/**/*`

The helmfile checks take `environments` (default `["production"]`) and run once per
environment. Each release renders from a tree holding only `helmfile.yaml.gotmpl`, that
environment's `env.json`, its own chart and `k8s/charts`, so results cache per chart and per
environment. A release's render is the only exec that touches the network; `ValidateHelmfile`
lints on top of it with `--skip-deps`. `ValidatePolaris` and `ValidateKubeconform` audit the same
render, so it is shared by all four checks.

#### Test Checks
- `TestGo(source, paths)` - Run Go tests (delegates to GoModule.Test)
- `TestPython(source, paths)` - Run Python tests (delegates to PythonProject.Test).
  Passes no `-m`: which markers an ordinary run excludes is each project's own
  `addopts`. See Kubernetes integration tests.

### Format Functions (`+generate`, auto-apply)
These also run as part of `dagger check` (a non-empty changeset fails the check).
- `FormatNix(source, paths)` - Format Nix files (`dagger call format-nix --auto-apply`)
- `LintGo(source)` - `go mod tidy` + `golangci-lint run --fix` for each Go module;
  fails if issues remain that `--fix` can't resolve (e.g. cyclop, gosec)
  (`dagger call lint-go --auto-apply`)
- `FormatPython(source, paths)` - Run `black` on each Python project
  (`dagger call format-python --auto-apply`)
- `FormatCue(source)` / `FixCue(source)` / `ExportCue(source)` - CUE formatting,
  syntax upgrades, and `config/gen/<env>/env.json` export
- `UpdateGoVendorHash(source)` - Recompute the nix buildGoModule vendorHash for `cmd/lab`
  into `cmd/lab/gomod.json` (`dagger call update-go-vendor-hash --auto-apply`).
  Renovate runs this as a post-upgrade task whenever it bumps a `cmd/lab` Go dependency.

### Other Functions
- `BuildCliGo(source)` - Build lab CLI (using Go, faster)
- `Cli(source, platform)` - Get lab binary (Go build)
- `CliNix(source)` - Get lab binary (Nix build)

## Kubernetes integration tests

Some things cannot be checked by rendering a chart: whether reflector actually
copies a ConfigMap, for instance. `TestKubernetesIntegration` starts a
throwaway single-node k3s cluster, deploys the releases under test onto it with
this repo's own helmfile, waits for them to become healthy, and runs each
release's pytest suite against the result — then destroys the cluster.

```bash
# Deploy and test everything the ephemeral environment enables
dagger call test-kubernetes-integration

# Narrow that to specific releases
dagger call test-kubernetes-integration --releases=reflector

# Same run, but keep the JUnit XML reports
dagger call kubernetes-integration-reports export --path=./reports

# Sync twice before testing, to check that re-syncing an existing cluster works
dagger call test-kubernetes-integration --repeat-sync
```

The workflow is repeatable with no manual steps: every run gets a fresh cluster
under a new random name and destroys it again, so running it twice in a row
needs nothing in between.

It is **not** a `+check`, on purpose, and nothing in `dagger check` runs it. It
needs an engine that allows privileged execs, and `dagger check` runs whatever a
PR contains: a PR that changes `.woodpecker/ci.yaml`, this module or the pytest
suites it runs could use a privileged exec to escape the container. It runs
only when someone invokes it, on an engine that someone has chosen to give
`insecureRootCapabilities` — and the intent is to run it on trusted refs only,
after a merge and before a deploy, rather than on PRs. The Python check keeps
passing on a machine with no cluster at all.

### Prerequisites

The cluster is a `k3s server` run as a Dagger service, and `k3s` cannot run
containers without privileges: it starts containerd and a kubelet, which
create cgroups and mount filesystems. The engine therefore has to allow
privileged execs:

```json
{ "security": { "insecureRootCapabilities": true } }
```

`k8s/platform/dagger-engine/values.yaml` sets this to `false`, so the in-cluster
engine cannot run the service. It does not reject it outright: `k3s` just never
comes up, so the service start is bounded at 3 minutes and the workflow then
fails naming this prerequisite rather than hanging. Run it against an engine
that allows privileged execs — a local Docker-based engine is the usual answer:

```bash
unset _EXPERIMENTAL_DAGGER_RUNNER_HOST   # don't use the in-cluster engine
dagger call test-kubernetes-integration
```

There is no Docker daemon, k3d or `DOCKER_HOST` involved: nothing outside the
engine is needed.

### Networking and credentials

```
Dagger exec (helmfile, kubectl, pytest)
  │  KUBECONFIG=/run/k3s/kubeconfig → https://k3s:6443
  ▼
k3s service, bound at the alias "k3s"
  └─ 6443  the API server
```

A kubeconfig pointing at `localhost` is useless inside a Dagger container, so
the API server's certificate is issued for the service's alias
(`--tls-san k3s`) and the kubeconfig names that alias.

The kubeconfig is built by the workflow, not taken from k3s. The API server is
started with `--kube-apiserver-arg token-auth-file=...`, naming a file that
holds a random per-run token mapped to `system:masters`; the file is a Dagger
secret mounted into the service. The client side gets its CA from `/cacerts`,
the one endpoint k3s serves without credentials, and `kubectl config` assembles
a kubeconfig from that CA and the token. The fetch of `/cacerts` is the only
request that does not verify the server — it is what supplies the CA — and it
stays inside the engine's service network. Every call after it verifies.

### Kubeconfig isolation

The admin kubeconfig only ever exists as a `*dagger.File` inside the engine,
mounted at `/run/k3s/kubeconfig` in the containers that need it. Concretely:

- No host file is read, written or merged, and no host context changes.
- The token is a Dagger secret, so it is masked in logs, and it is random per
  run, so a leaked one opens nothing once the service has stopped.
- Cluster names are random (`homelab-<12 hex>`), and the only thing the run
  ever stops is its own service.
- `Close` is deferred before the cluster exists, so a failed deploy, a failing
  test or a panic all still take the cluster down. It runs on a context
  detached from the caller's, with a one-minute limit of its own, because an
  interrupted run is exactly when the caller's context is already cancelled.

### Deploying with the existing helmfile

There is no separate state file for development clusters. The workflow runs
`helmfile sync` against `helmfile.yaml.gotmpl` with `--environment ephemeral`,
whose values come from `config/ephemeral.cue` by way of
`config/gen/ephemeral/env.json` like every other environment.

Release enablement is the `apps` map in those values, read by the helmfile
templates' `installedTemplate`. A `--selector` can narrow what a sync touches
but cannot turn a release on, which makes the environment the only usable source
of truth — so that is where the workflow gets its release set from, and
`--releases` only narrows it. Asking for a release the environment disables is an
error naming the file to edit, rather than a sync that silently installs nothing.

`ephemeral` therefore disables everything by default (`_appsDisabled` in
`config/base.cue`, derived from the full production list) and enables only what
has tests.

Helmfile is given the ephemeral kubeconfig through `KUBECONFIG` *and* the
cluster's own context through `--kube-context`. Between them there is nothing
left for an ambient current-context to decide: a kubeconfig that is not this
cluster's has no context by that name, and helmfile fails rather than deploying
somewhere else.

### Health, before tests

The cluster is not used until the API server answers `/readyz` and the node is
`Ready`. `helmfile sync` returning successfully then only means the manifests
were accepted, so the workflow waits on Kubernetes itself — `kubectl rollout
status` over every Deployment, StatefulSet and DaemonSet in the release's
namespace, with a 5m timeout. Namespace-scoped and workload-agnostic, so a
release whose workload isn't a Deployment needs nothing added; a namespace with
none of the three fails, which is the right answer for a release whose tests are
about to run. When the wait fails, the namespace's pod status, pod detail and
events are attached to the error.

The waits for several releases run concurrently, so their timeouts don't sum.

Only then does it prove the kubeconfig still names the cluster this run created:
same API endpoint, same context, and the same `kube-system` namespace UID that
was read when the cluster came up. The UID is the part a kubeconfig edit cannot
fake — a different cluster answering at the same address has a different one.

### How the markers keep production safe

A developer's shell very often has `KUBECONFIG` pointing at the real cluster,
and these tests create and delete namespaces. So they never run because a
kubeconfig happens to be valid; they run because someone selected them.

- Every test carries `@pytest.mark.kubernetes`, registered in the project's
  `pyproject.toml`.
- That same `pyproject.toml` sets `addopts = "-m 'not kubernetes'"`. This is the
  mechanism — it covers a bare `pytest` in an editor or a shell as well as
  `dagger check test-python`, and it is the only place that *can* cover the
  former.
- `PythonProject.Test` deliberately passes **no** `-m`. A command-line `-m`
  replaces `addopts` rather than narrowing it, so one passed from the runner
  would re-enable the tests a project deselected for itself and deselect the ones
  a project runs on purpose — `jellyfin-exporter` registers `integration`
  precisely so those tests run by default against its stub server.
- Deselecting every test in a project makes pytest exit 5. `Test` treats that as
  a pass, which is how a tests-only project with nothing left to run doesn't
  fail the Python check.
- The integration workflow selects `-m kubernetes` explicitly, and a run that
  collected nothing is reported as a failure rather than a pass.

The trade-off is that a new test project which forgets the `addopts` line is not
caught statically. It fails loudly instead: the ci toolchain has no Kubernetes
client and no `KUBECONFIG`, so the suite errors out in `dagger check` rather than
reaching any cluster.

Within the suite, missing credentials, an unreachable API server or a rejected
credential raise rather than skip: once the tests have been selected, not
running is a failure.

### Adding integration tests to another release

1. Create `k8s/<tier>/<release>/tests/` as a Python project — copy the shape of
   `k8s/foundation/reflector/tests/`: a `pyproject.toml` registering the
   `kubernetes` marker and deselecting it by default, a `uv.lock`, a
   `conftest.py` loading `KUBECONFIG`, and the tests themselves.
2. Mark every test `@pytest.mark.kubernetes`.
3. Add `tests/` to the chart's `.helmignore`, so test files stay out of the
   packaged chart.
4. Enable the release in `config/ephemeral.cue`, then
   `dagger call export-cue --auto-apply`.
5. `dagger call test-kubernetes-integration --releases=<release>`.

Nothing in `.dagger` needs changing, and step 4 is what adds the release to the
default run: with no `--releases`, the workflow deploys and tests everything the
ephemeral environment enables. The workflow finds the test directory from the
release's chart path, deploys the release, waits on its namespace and runs
pytest; what to assert is entirely the test project's business.

### Caching

The call is cached at four levels, and the workflow is arranged so each one can
do its job.

1. **The outer function call.** Dagger caches a function's result on its
   arguments. `source` is a content-addressed directory filtered by the
   `+ignore` list, so re-running with nothing changed in
   `helmfile.yaml.gotmpl`, the ephemeral environment's values or anything under
   `k8s/` returns the earlier verdict without starting a cluster. Editing
   anything outside that list (`README.md`, `.woodpecker/`, `nix/`) is also a
   hit. The filter is coarse — it cannot know which releases a run will deploy —
   so a change to *any* chart under `k8s/` re-runs this level, which costs only
   the plan.
2. **The inner function call.** `TestKubernetesIntegration` and
   `KubernetesIntegrationReports` plan against the whole source, then pass
   `RunKubernetesIntegration` only what the run reads: the state file, the
   ephemeral values, the shared charts and the chosen releases' charts (tests
   included). It is called through the module's own API, which needs the
   experimental self-calls capability (`dagger develop --with-self-calls`, kept
   in `dagger.json`). The workflow itself therefore re-runs only when something
   it deploys or tests changes.
3. **Execs that do not touch the cluster.** The toolchain, the helmfile planning
   (`helmfile build` and `list`) and each test project's `uv sync` are all built
   *before* anything cluster-specific is applied to the container, so they are
   cached whatever the cluster looks like. The `uv sync` copies in only
   `pyproject.toml` and `uv.lock` before installing, and runs on the toolchain
   *without* the repo mounted — a mount is part of an exec's cache key — so
   editing a test reinstalls nothing.
4. **Execs that do.** Everything from the first use of the service on is unique
   to the run, by design: `HOMELAB_K3S_CLUSTER=<random name>` is in its
   environment, and the kubeconfig it mounts carries a random token. Caching one
   would mean serving "helmfile sync succeeded" for a cluster that has since been
   destroyed.

`RunKubernetesIntegration` returns plain data — strings, with the JUnit reports
as XML text — and never a `Directory`. A directory built from execs that used a
service and a secret cannot outlive the session that made it, and a function
result holding one is silently not cached across sessions: the inner call
re-ran every time until it returned text instead.

What persists *between* runs that are not cached is the containerd root, in a
`PRIVATE` cache volume (about 540MB with reflector's images in it). The k3s
image itself is an ordinary image layer, cached by the engine.

Measured on engine v0.21.10, one release (reflector), same host:

| Run | k3d in dind | k3s service |
| --- | --- | --- |
| Nothing changed | 2.8s | 3s |
| File outside the `+ignore` list changed | — | 4s |
| An unrelated chart changed | 107s | 3s |
| A reflector test changed | 99s | 55s |
| First run, warm toolchain, empty volumes | — | 91s |

A run that is not cached spends roughly: 8s for k3s to boot and the node to go
Ready, 19s in `helmfile sync`, 11s in `rollout status`, 5s in pytest and the rest
in loading the module and planning.

### Limitations

- Needs an engine that allows privileged execs, which the in-cluster engine
  does not. See Prerequisites.
- k3s's containerd root is a `PRIVATE` cache volume, keyed on the k3s image tag.
  It has to be a real filesystem for overlayfs, and two containerds cannot share
  a root. It is deliberately not wiped between runs, so it is what makes images
  pulled once stay pulled; a run killed mid-flight leaves its containers' metadata
  in it for the next k3s to find.
- `k3sImage` is pinned to match `services.k3s.package` in
  `nix/modules/k3s/k3s.nix`, so the tests run against the Kubernetes version the
  real cluster does.
- Dagger's exec cache keys on the command and the filesystem, neither of which
  captures that an exec against a service depends on live state. Every exec that
  touches the cluster therefore carries `HOMELAB_K3S_CLUSTER=<random name>`,
  which makes it unique to that run.

## Caching & Performance

### Two-Layer Caching Model

Dagger provides two layers of caching that work together:

**Layer 2 — Dagger function call cache**: Caches the return value of a Dagger
function based on the function identity and its arguments (including source
directory content hash). When `dagger check test-go` runs and the filtered Go
source hasn't changed, the entire TestGo result is returned from cache (~0.5s).

**Layer 1 — BuildKit content-addressed cache**: Caches individual container
operations (exec, mount, copy) based on the content of their inputs. Within one
TestGo run, each GoModule.Test() independently checks the BuildKit cache.
Modules with unchanged source directories hit the cache while only changed
modules re-execute.

### How Caching Interacts with the Module Pattern

The per-project module pattern maximizes cache efficiency:

```
dagger check test-go
├─ Layer 2 cache hit? → Return cached result (0.5s)
└─ Layer 2 cache miss → TestGo() runs:
   ├─ GoModule{.dagger}.Test()       → Layer 1 cache hit (unchanged)
   ├─ GoModule{cmd/lab}.Test()       → Layer 1 cache MISS (file changed)
   ├─ GoModule{homepage/...}.Test()  → Layer 1 cache hit (unchanged)
   └─ GoModule{forgejo/...}.Test()     → Layer 1 cache hit (unchanged)
```

```
TestGo() fans out over the discovered modules
├─ GoModule{.dagger}.Test()       → Layer 1 cache hit (unchanged)
├─ GoModule{cmd/lab}.Test()       → Layer 1 cache MISS (file changed)
├─ GoModule{homepage/...}.Test()  → Layer 1 cache hit (unchanged)
└─ GoModule{forgejo/...}.Test()     → Layer 1 cache hit (unchanged)
```

### Pre-Call Filtering (`+ignore`)

All functions use `+ignore` annotations to filter the source directory before execution. This provides optimal caching — changes to unrelated files don't invalidate the cache.

```go
// +defaultPath="/"
// +ignore=["*", "!**/*.nix", ".devenv*", ".devenv/**", "devenv.local.*"]
source *dagger.Directory,
```

- `"*"` — ignore everything by default
- `"!pattern"` — un-ignore (include) matching files
- Additional patterns after `!` re-ignore specific paths

**Best practice**: include only files the function actually reads. Test by changing
an unrelated file and verifying the check uses its cache.

**Example**: When you change a `.go` file in `cmd/lab/`:
- ✅ `TestGo()` cache invalidates (includes `**/*.go`)
- ✅ `BuildCli()` cache invalidates (includes `cmd/lab/**/*`)
- ❌ `ValidateNix()` cache remains valid (only includes `**/*.nix`)
- ❌ `LintYaml()` cache remains valid (only includes `**/*.yaml`)
- And within TestGo(), only the `cmd/lab` GoModule re-tests (Layer 1)

### Per-Project Source Scoping

Each module type scopes its source differently based on project characteristics:

| Type | Source Scope | Reason |
|---|---|---|
| GoModule | Per-module directory | Go modules are self-contained |
| PythonProject | Per-project directory | Python projects are self-contained |
| helmfile release | Chart directory + `helmfile.yaml.gotmpl` + one environment's `env.json` | Helmfile reads the state file and environment values for every release |
| TerraformModule | Full `terraform/` directory | Modules reference siblings via relative paths |

### Cache Behavior Examples

```bash
# First run - runs all checks
dagger check

# Change a .nix file - only Nix checks/generators re-run
echo "# comment" >> nix/hosts/common/default.nix
dagger check  # Only FormatNix + ValidateNix re-run

# Change Go code in one module - only that module re-tests
echo "// comment" >> cmd/lab/main.go
dagger check  # LintGo + TestGo re-run, but only cmd/lab module actually re-executes

# Change a Python file in one project
echo "# comment" >> k8s/foundation/kured/files/kured-webhook/server.py
dagger check test-python  # Only the kured-webhook project re-executes
```

## Development

### Check Function Semantics (`+check`)

Functions annotated with `// +check` are run via `dagger check`. They only fail when
they return a non-nil Go error. Understanding return types is important:

| Return type | Behavior |
|---|---|
| `(string, error)` | Pass/fail only. Return error to fail, message string on success. |
| `(*dagger.Directory, error)` | **Silently passes** even if the directory differs from source. Dagger does NOT auto-diff returned directories against the workspace. |
| `(*dagger.Changeset, error)` | Same as Directory — a non-empty changeset does NOT auto-fail the check. |

Returning a modified `*dagger.Directory` or non-empty `*dagger.Changeset` with nil
error always passes the check silently. You must explicitly detect changes and return
an error to fail.

### Auto-Apply (`--auto-apply`)

The `--auto-apply` flag automatically exports changesets to the working directory.

| Command | Behavior |
|---|---|
| `dagger call format-foo --auto-apply` | ✅ Applies changeset to working directory |
| `dagger call format-foo export --path=.` | ✅ Same effect, explicit export |
| `dagger check lint-foo --auto-apply` | ❌ `dagger check` ignores `--auto-apply` |
| `dagger call lint-foo --auto-apply` (with error return) | ❌ Go error blocks changeset export |

### Formatter Pattern (check + fix)

For functions that format code (alejandra, go fmt, black, etc.), a single function
cannot both fail the check AND support `--auto-apply`. A Go error return blocks
`--auto-apply` from applying the changeset. The solution is two functions plus a
shared helper:

1. **Check function** (`+check`, returns `string, error`) — detects if files need
   formatting by comparing formatted output to source. Returns error listing changed
   files.

2. **Format function** (returns `*dagger.Changeset`) — formats files and returns the
   changeset without error, so `--auto-apply` can apply it.

3. **Shared helper** (private, returns `*dagger.Directory`) — contains the actual
   formatting logic, used by both.

```go
// LintFoo validates Foo formatting.
// +check
func (m *Homelab) LintFoo(ctx context.Context, source *dagger.Directory) (string, error) {
    formatted := m.fooFormat(source)
    changeset := formatted.Changes(source)
    empty, err := changeset.IsEmpty(ctx)
    if err != nil {
        return "", fmt.Errorf("checking for changes: %w", err)
    }
    if !empty {
        modified, _ := changeset.ModifiedPaths(ctx)
        return "", fmt.Errorf("files need formatting: %s\nRun `dagger call format-foo --auto-apply` to fix",
            strings.Join(modified, ", "))
    }
    return "Foo lint passed", nil
}

// FormatFoo formats files. Use `dagger call format-foo --auto-apply` to apply.
func (m *Homelab) FormatFoo(source *dagger.Directory) *dagger.Changeset {
    return m.fooFormat(source).Changes(source)
}

func (m *Homelab) fooFormat(source *dagger.Directory) *dagger.Directory {
    return dag.Container().From("...").
        WithMountedDirectory("/src", source).
        WithExec([]string{"formatter", "."}).
        Directory("/src")
}
```

When a check runs multiple formatters, order matters. Run destructive tools (that
remove code) before cosmetic tools (that reformat):

1. **deadnix** (removes dead code — can leave bad formatting)
2. **alejandra** (reformats — cleans up after deadnix)

Same principle applies to other language stacks: run linters that modify structure
before formatters that fix style.

### Per-Project Module Pattern

When adding a new language/tool type, follow this pattern:

1. **Define the struct** with `Path` and `Source` fields:
   ```go
   type FooProject struct {
       Path   string
       Source *dagger.Directory
   }
   ```

2. **Add a discovery method** on Homelab that returns scoped instances:
   ```go
   func (m *Homelab) FooProjects(source *dagger.Directory) []*FooProject {
       // Use source.Directory(path) to scope each project
   }
   ```

3. **Add per-project methods** with `+check`:
   ```go
   func (fp *FooProject) Test(ctx context.Context) (string, error) { ... }
   ```

4. **Add aggregate top-level methods** for `dagger check`:
   ```go
   func (m *Homelab) TestFoo(ctx context.Context, source *dagger.Directory) (string, error) {
       // Iterate and delegate to per-project methods with errgroup
   }
   ```

5. **Update the constructor** to discover projects at init time.

### Validation-Only Pattern

For checks that only validate without modifying files (e.g., `go vet`, `helm lint`,
`nix flake check`), return `(string, error)` directly:

```go
// +check
func (m *Homelab) ValidateFoo(ctx context.Context, source *dagger.Directory) (string, error) {
    _, err := dag.Container().From("...").
        WithMountedDirectory("/src", source).
        WithExec([]string{"validator", "--check"}).
        Sync(ctx)
    if err != nil {
        return "", fmt.Errorf("validation failed: %w", err)
    }
    return "Validation passed", nil
}
```

### Adding New Functions

1. Add function to the appropriate file with `+ignore` filters:
   ```go
   func (m *Homelab) MyNewFunction(
       ctx context.Context,
       // +defaultPath="/"
       // +ignore=["*", "!path/to/relevant/**/*"]
       source *dagger.Directory,
   ) (string, error) {
       // Implementation
   }
   ```

2. Regenerate SDK:
   ```bash
   dagger develop
   ```

3. Test:
   ```bash
   dagger call my-new-function --source=.
   ```

**Filter Best Practices**:
- Include only files the function actually reads
- Use specific paths over broad wildcards
- Test that changes to unrelated files don't invalidate cache
- Document filters in function comments

### Testing Changes

```bash
# Quick test with Go build
dagger call build-cli-go --source=.

# Full test with Nix build (slower but production-accurate)
dagger call build-cli --source=.
```

## Troubleshooting

### "cannot find package" errors
Run `dagger develop` to regenerate SDK code.

### SDK version mismatch
```bash
rm -rf internal/ dagger.gen.go querybuilder/
dagger develop
```

### Build failures
Check that you're running from the repository root and passing `--source=.`

### Container runtime errors
Ensure Docker is running:
```bash
docker info
```

### Terraform validation failures
Some Terraform modules may fail validation due to:
- Lock file version mismatches (fix with `tofu init -upgrade` locally)
- Missing variable declarations
- Cross-module reference issues

These are surfaced honestly now — the previous implementation suppressed all
Terraform errors with `|| true`.

## Files

- `main.go` - Main module: Homelab struct, constructor, Nix/CUE/YAML/Woodpecker/CLI functions
- `golang.go` - GoModule struct and Go-specific functions
- `python.go` - PythonProject struct and Python-specific functions
- `helm.go` - Chart discovery and path matching
- `helmfile.go` - helmfileRelease struct and helmfile functions
- `kubernetes.go` - Polaris and Kubeconform on rendered releases
- `k3s.go` - Ephemeral k3s cluster lifecycle for the integration workflow
- `integration.go` - The Kubernetes integration workflow
- `junit.go` - pytest JUnit XML parsing
- `terraform.go` - TerraformModule struct and Terraform-specific functions
- `containers.go` - Container image constants and helpers
- `paths.go` - Path filtering utilities
- `go.mod` - Go module dependencies (Dagger SDK)
- `internal/` - Auto-generated Dagger SDK (gitignored)
- `dagger.gen.go` - Auto-generated type definitions (gitignored)
- `querybuilder/` - Auto-generated query builders (gitignored)
- `.gitignore` - Ignores auto-generated files

## References

- [Dagger Documentation](https://docs.dagger.io/)
- [Dagger Go SDK](https://docs.dagger.io/sdk/go)
- [CI Architecture](../docs/ci-architecture.md)
