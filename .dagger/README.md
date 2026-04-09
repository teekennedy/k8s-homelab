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
maximize cache granularity. Each struct (GoModule, PythonProject, HelmChart,
TerraformModule) carries a scoped source directory containing only its project's
files. This enables two levels of caching:

- **Layer 2 (Dagger function call cache)**: `dagger check test-go` caches the
  entire TestGo result. If the filtered source (all `**/*.go` files) hasn't
  changed, the check returns instantly (~0.5s).

- **Layer 1 (BuildKit content-addressed cache)**: Within the per-module calls
  (`dagger call go-modules test`), each module's Test() runs against a scoped
  subdirectory. Unchanged modules hit the BuildKit exec cache while only changed
  modules re-run.

The combination means:
- `dagger check` re-runs the fewest checks possible for any given file change
- `dagger call <type> <method>` re-tests only the changed projects within a type

### File Organization

| File | Contents |
|---|---|
| `main.go` | Homelab struct, constructor, Nix/CUE/YAML/Woodpecker/CLI functions |
| `golang.go` | GoModule struct, per-module Test/Lint, aggregate LintGo/TestGo/FormatGo |
| `python.go` | PythonProject struct, per-project Test/Lint, aggregate LintPython/TestPython/FormatPython |
| `helm.go` | HelmChart struct, per-chart Validate/Build, aggregate ValidateHelm/BuildHelm |
| `terraform.go` | TerraformModule struct, per-module Validate, aggregate ValidateTerraform |
| `containers.go` | Container image constants and helpers |
| `paths.go` | Path filtering utilities |
| `tests.go` | Integration tests |

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
dagger check lint-nix
dagger check build-helm

# Per-project operations (maximum cache granularity)
dagger call go-modules test           # Test each Go module independently
dagger call go-modules lint           # Lint each Go module independently
dagger call python-projects test      # Test each Python project independently
dagger call python-projects lint      # Lint each Python project independently
dagger call helm-charts validate      # Validate each Helm chart independently
dagger call helm-charts build         # Render each Helm chart independently
dagger call terraform-modules validate # Validate each Terraform module independently

# List discovered projects
dagger call go-modules                # Show all Go modules
dagger call python-projects           # Show all Python projects
dagger call helm-charts               # Show all Helm charts
dagger call terraform-modules         # Show all Terraform modules

# Auto-apply formatting fixes (use format-* functions, not check)
dagger call format-nix --auto-apply
dagger call format-go --auto-apply
dagger call format-python --auto-apply

# Build lab CLI
dagger call build-cli --source=.        # Using Nix (production)
dagger call build-cli-go --source=.     # Using Go (faster)

# Get built binary
dagger call cli-nix --source=. export --path=./lab
```

### Via lab CLI

```bash
lab ci               # Run all checks
lab ci lint          # Run all lint* checks
lab ci lint --fix    # Run lint checks, auto-apply formatting fixes
lab ci build         # Run all build* checks
lab ci test          # Run all test* checks
lab ci validate      # Run all validate* checks
```

## Available Functions

All check functions use pre-call filtering for optimal caching. Only relevant files are included.
Functions annotated with `// +check` can be run via `dagger check`.

### Per-Project Module Types

#### GoModule
Discovered automatically from `go.mod` files. Each module gets a scoped source directory.
- `Test()` - Run `go test` for this module (`+check`)
- `Lint()` - Run `go vet` and `go fmt` for this module (`+check`)

#### PythonProject
Discovered automatically from `pyproject.toml` files.
- `Test()` - Run `pytest` for this project (`+check`)
- `Lint()` - Run `black --check` for this project (`+check`)
- `Format()` - Format with `black`, returning the formatted directory

#### HelmChart
Discovered automatically from `Chart.yaml` files under `k8s/`.
- `Validate()` - Run `helm lint` for this chart (`+check`)
- `Build()` - Run `helm template` for this chart (`+check`)

#### TerraformModule
Discovered automatically from `.tf` files under `terraform/`.
Uses the full `terraform/` directory as source since modules can reference
siblings via relative paths (e.g., `../k8s-secret`).
- `Validate()` - Run `tofu init` + `tofu validate` for this module (`+check`)

### Top-Level Checks

#### Lint Checks
- `LintNix(source, paths)` - Nix formatting and dead code validation
  - Filters: `**/*.nix`
  - Tools: `deadnix`, `alejandra`
  - Fix: `dagger call format-nix --auto-apply`
- `LintGo(source)` - Go linting and formatting validation (delegates to GoModule.Lint)
  - Tools: `go vet`, `go fmt`
  - Fix: `dagger call format-go --auto-apply`
- `LintCue(source, paths)` - CUE validation
  - Filters: `config/**/*.cue`
- `LintPython(source, paths)` - Python formatting validation (delegates to PythonProject.Lint)
  - Tools: `black`
  - Fix: `dagger call format-python --auto-apply`
- `LintYaml(source, paths)` - YAML linting
  - Filters: `**/*.yaml`, `**/*.yml`, `.yamllint.yaml`

#### Validate Checks
- `ValidateNix(source)` - Nix flake check
  - Filters: `flake.nix`, `flake.lock`, `nix/**/*`
- `ValidateHelm(source, paths)` - Helm chart validation (delegates to HelmChart.Validate)
  - Filters: `k8s/**/*`
- `ValidateTerraform(source, paths)` - Terraform/OpenTofu validation (delegates to TerraformModule.Validate)
  - Filters: `terraform/**/*`
- `ValidateWoodpecker(source, paths)` - Woodpecker CI pipeline validation
  - Filters: `.woodpecker/*.yaml`

#### Build Checks
- `BuildCli(source)` - Build lab CLI (using Nix)
  - Filters: `cmd/lab/**/*`
- `BuildHelm(source, paths)` - Render Helm templates (delegates to HelmChart.Build)
  - Filters: `k8s/**/*`

#### Test Checks
- `TestGo(source, paths)` - Run Go tests (delegates to GoModule.Test)
- `TestPython(source, paths)` - Run Python tests (delegates to PythonProject.Test)

### Format Functions (auto-apply)
- `FormatNix(source, paths)` - Format Nix files (`dagger call format-nix --auto-apply`)
- `FormatGo(source)` - Format Go files (`dagger call format-go --auto-apply`)
- `FormatPython(source, paths)` - Format Python files (`dagger call format-python --auto-apply`)

### Other Functions
- `BuildCliGo(source)` - Build lab CLI (using Go, faster)
- `Cli(source, platform)` - Get lab binary (Go build)
- `CliNix(source)` - Get lab binary (Nix build)

## Caching & Performance

### Two-Layer Caching Model

Dagger provides two layers of caching that work together:

**Layer 2 — Dagger function call cache**: Caches the return value of a Dagger
function based on the function identity and its arguments (including source
directory content hash). When `dagger check test-go` runs and the filtered Go
source hasn't changed, the entire TestGo result is returned from cache (~0.5s).

**Layer 1 — BuildKit content-addressed cache**: Caches individual container
operations (exec, mount, copy) based on the content of their inputs. When
`dagger call go-modules test` runs, each GoModule.Test() independently checks
the BuildKit cache. Modules with unchanged source directories hit the cache
while only changed modules re-execute.

### How Caching Interacts with the Module Pattern

The per-project module pattern maximizes cache efficiency:

```
dagger check test-go
├─ Layer 2 cache hit? → Return cached result (0.5s)
└─ Layer 2 cache miss → TestGo() runs:
   ├─ GoModule{.dagger}.Test()       → Layer 1 cache hit (unchanged)
   ├─ GoModule{cmd/lab}.Test()       → Layer 1 cache MISS (file changed)
   ├─ GoModule{homepage/...}.Test()  → Layer 1 cache hit (unchanged)
   └─ GoModule{gitea/...}.Test()     → Layer 1 cache hit (unchanged)
```

```
dagger call go-modules test
├─ GoModule{.dagger}.Test()       → Layer 1 cache hit (unchanged)
├─ GoModule{cmd/lab}.Test()       → Layer 1 cache MISS (file changed)
├─ GoModule{homepage/...}.Test()  → Layer 1 cache hit (unchanged)
└─ GoModule{gitea/...}.Test()     → Layer 1 cache hit (unchanged)
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
- ✅ `LintGo()` cache invalidates (includes `**/*.go`)
- ✅ `BuildCli()` cache invalidates (includes `cmd/lab/**/*`)
- ❌ `LintNix()` cache remains valid (only includes `**/*.nix`)
- ❌ `LintYaml()` cache remains valid (only includes `**/*.yaml`)
- And within LintGo(), only the `cmd/lab` GoModule re-lints (Layer 1)

### Per-Project Source Scoping

Each module type scopes its source differently based on project characteristics:

| Type | Source Scope | Reason |
|---|---|---|
| GoModule | Per-module directory | Go modules are self-contained |
| PythonProject | Per-project directory | Python projects are self-contained |
| HelmChart | Per-chart directory | Charts are self-contained (deps fetched from registries) |
| TerraformModule | Full `terraform/` directory | Modules reference siblings via relative paths |

### Cache Behavior Examples

```bash
# First run - runs all checks
dagger check

# Change a .nix file - only Nix checks re-run
echo "# comment" >> nix/hosts/common/default.nix
dagger check  # Only LintNix + ValidateNix re-run

# Change Go code in one module - only that module re-tests
echo "// comment" >> cmd/lab/main.go
dagger check  # LintGo + TestGo re-run, but only cmd/lab module actually re-executes

# Change a Python file in one project
echo "# comment" >> k8s/foundation/kured/files/kured-webhook/server.py
dagger call python-projects test  # Only kured-webhook project re-tests
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
- `helm.go` - HelmChart struct and Helm-specific functions
- `terraform.go` - TerraformModule struct and Terraform-specific functions
- `containers.go` - Container image constants and helpers
- `paths.go` - Path filtering utilities
- `tests.go` - Integration tests
- `go.mod` - Go module dependencies (Dagger SDK)
- `internal/` - Auto-generated Dagger SDK (gitignored)
- `dagger.gen.go` - Auto-generated type definitions (gitignored)
- `querybuilder/` - Auto-generated query builders (gitignored)
- `.gitignore` - Ignores auto-generated files

## References

- [Dagger Documentation](https://docs.dagger.io/)
- [Dagger Go SDK](https://docs.dagger.io/sdk/go)
- [CI Architecture](../docs/ci-architecture.md)
