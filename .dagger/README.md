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

# Auto-apply formatting fixes from lint changesets
dagger check 'lint*' --auto-apply

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
- `LintNix(source, paths)` - Nix formatting and dead code removal (returns changeset)
  - Filters: `**/*.nix`
  - Tools: `alejandra`, `deadnix --edit`
- `LintGo(source, paths)` - Go linting and formatting (returns changeset)
  - Tools: `go vet`, `go fmt`
- `LintCue(source, paths)` - CUE validation
  - Filters: `config/**/*.cue`
- `LintPython(source, paths)` - Python formatting with black (returns changeset)
  - Tools: `black`
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

### Other Functions
- `BuildCliGo(source)` - Build lab CLI (using Go, faster)
- `Cli(source, platform)` - Get lab binary (Go build)
- `CliNix(source)` - Get lab binary (Nix build)

## Caching & Performance

### Pre-Call Filtering

All functions use `+ignore` annotations to filter the source directory before execution. This provides optimal caching:

**Syntax**: `// +ignore=["*", "!pattern1", "!pattern2"]` where:
- `"*"` ignores all files by default
- `"!pattern"` negates the ignore (includes matching files)
- Patterns support globs like `**/*.go`, `cmd/lab/**/*`

**Example**: When you change a `.go` file in `cmd/lab/`:
- ✅ `LintGo()` cache invalidates (includes `cmd/lab/**/*.go` via `!cmd/lab/**/*.go`)
- ✅ `BuildCli()` cache invalidates (includes `cmd/lab/**/*` via `!cmd/lab/**/*`)
- ❌ `LintNix()` cache remains valid (only includes `**/*.nix`)
- ❌ `LintYaml()` cache remains valid (only includes `**/*.yaml`)

**Benefits**:
- Faster CI runs - only affected checks run
- Better caching - unrelated changes don't invalidate cache
- Parallel execution - independent checks can run simultaneously

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
