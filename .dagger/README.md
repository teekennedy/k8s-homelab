# Homelab Dagger Module

This Dagger module provides CI/CD functionality for k8s-homelab.

## Architecture

This module is **independent of the lab CLI** to avoid circular dependencies:
- Dev shell (`devenv shell`) includes lab pre-built
- CI container (`containers.ci`) does NOT include lab
- This module builds lab from scratch as part of the pipeline

See `../docs/ci-architecture.md` for details.

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

   # Should show: build-cli, lint, validate, etc.
   ```

### After Updating main.go

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

### Checks

#### Lint Checks
- `LintNix(source, paths)` - Nix formatting and dead code validation
  - Filters: `**/*.nix`
  - Tools: `deadnix`, `alejandra`
  - Fix: `dagger call format-nix --auto-apply`
- `LintGo(source)` - Go linting and formatting validation
  - Tools: `go vet`, `go fmt`
  - Fix: `dagger call format-go --auto-apply`
- `LintCue(source, paths)` - CUE validation
  - Filters: `config/**/*.cue`
- `LintPython(source, paths)` - Python formatting validation
  - Tools: `black`
  - Fix: `dagger call format-python --auto-apply`
- `LintYaml(source, paths)` - YAML linting
  - Filters: `**/*.yaml`, `**/*.yml`, `.yamllint.yaml`

#### Validate Checks
- `ValidateNix(source)` - Nix flake check
  - Filters: `flake.nix`, `flake.lock`, `nix/**/*`
- `ValidateHelm(source, paths)` - Helm chart validation
  - Filters: `k8s/**/*`
- `ValidateTerraform(source, paths)` - Terraform/OpenTofu validation
  - Filters: `terraform/**/*`
- `ValidateWoodpecker(source, paths)` - Woodpecker CI pipeline validation
  - Filters: `.woodpecker/*.yaml`

#### Build Checks
- `BuildCli(source)` - Build lab CLI (using Nix)
  - Filters: `cmd/lab/**/*`
- `BuildHelm(source, paths)` - Render Helm templates
  - Filters: `k8s/**/*`

#### Test Checks
- `TestGo(source, paths)` - Run Go tests
- `TestPython(source, paths)` - Run Python tests with pytest

### Format Functions (auto-apply)
- `FormatNix(source, paths)` - Format Nix files (`dagger call format-nix --auto-apply`)
- `FormatGo(source)` - Format Go files (`dagger call format-go --auto-apply`)
- `FormatPython(source, paths)` - Format Python files (`dagger call format-python --auto-apply`)

### Other Functions
- `BuildCliGo(source)` - Build lab CLI (using Go, faster)
- `Cli(source, platform)` - Get lab binary (Go build)
- `CliNix(source)` - Get lab binary (Nix build)

## Caching & Performance

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

### Cache Behavior

```bash
# First run - runs all checks
dagger check

# Change a .nix file - only Nix checks re-run
echo "# comment" >> nix/hosts/common/default.nix
dagger check  # Only LintNix + ValidateNix re-run

# Change Go code - only Go checks re-run
echo "// comment" >> cmd/lab/main.go
dagger check  # Only LintGo + BuildCli + TestGo re-run
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

1. Add function to `main.go` with appropriate filters:
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

## Files

- `main.go` - Main module implementation
- `go.mod` - Go module dependencies (Dagger SDK)
- `internal/` - Auto-generated Dagger SDK (gitignored)
- `dagger.gen.go` - Auto-generated type definitions (gitignored)
- `querybuilder/` - Auto-generated query builders (gitignored)
- `.gitignore` - Ignores auto-generated files

## References

- [Dagger Documentation](https://docs.dagger.io/)
- [Dagger Go SDK](https://docs.dagger.io/sdk/go)
- [CI Architecture](../docs/ci-architecture.md)
