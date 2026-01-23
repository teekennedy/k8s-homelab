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
# Full CI pipeline
dagger call all --source=.

# With auto-fix enabled (exports fixed source)
dagger call all --source=. --fix export --path=.

# Individual stages
dagger call lint --source=.
dagger call lint --source=. --fix export --path=.  # Auto-fix and export
dagger call validate --source=.
dagger call build --source=.
dagger call test --source=.

# Build lab CLI
dagger call build-cli --source=.        # Using Nix (production)
dagger call build-cli-go --source=.     # Using Go (faster)

# Get built binary
dagger call cli-nix --source=. export --path=./lab
```

### Via lab CLI

```bash
lab ci all           # Full pipeline
lab ci all --fix     # Full pipeline with auto-fix
lab ci build         # Build all components
lab ci lint          # Run linters (check only)
lab ci lint --fix    # Run linters and auto-fix issues
lab ci test          # Run tests
lab ci validate      # Validate configs
```

## Available Functions

All functions use pre-call filtering for optimal caching. Only relevant files are included:

- `All(source, fix)` - Run complete CI pipeline (lint → validate → build → test)
  - `fix`: Auto-fix linting issues (default: false)
- `Lint(source, fix)` - Run all linters (Nix, Go, CUE, YAML)
  - `fix`: Auto-fix issues where possible (default: false)
- `LintNix(source, fix)` - Nix linting and formatting
  - Filters: `**/*.nix`
  - Check: `alejandra --check`, `deadnix --fail`
  - Fix: `alejandra .`, `deadnix --edit .`
- `LintGo(source, fix)` - Go linting and formatting
  - Filters: `cmd/lab/**/*.go`, `cmd/lab/go.mod`, `cmd/lab/go.sum`
  - Check: `go vet`
  - Fix: `go fmt`
- `LintCue(source)` - CUE validation (no auto-fix)
  - Filters: `config/**/*.cue`
- `LintYaml(source)` - YAML linting (no auto-fix)
  - Filters: `**/*.yaml`, `**/*.yml`, `.yamllint.yaml`
- `Validate(source)` - Run all validation checks
- `ValidateNix(source)` - Nix flake check
  - Filters: `flake.nix`, `flake.lock`, `nix/**/*`, `cmd/lab/flake.nix`, `cmd/lab/flake.lock`
- `ValidateHelm(source)` - Helm chart validation
  - Filters: `k8s/**/*`
- `ValidateTerraform(source)` - Terraform/OpenTofu validation
  - Filters: `terraform/**/*`
- `Build(source)` - Build all artifacts
- `BuildCli(source)` - Build lab CLI (using Nix)
  - Filters: `cmd/lab/**/*`
- `BuildCliGo(source)` - Build lab CLI (using Go, faster)
  - Filters: `cmd/lab/**/*.go`, `cmd/lab/go.mod`, `cmd/lab/go.sum`
- `Test(source)` - Run all tests
- `TestGo(source)` - Run Go tests
  - Filters: `cmd/lab/**/*.go`, `cmd/lab/go.mod`, `cmd/lab/go.sum`
- `Cli(source, platform)` - Get lab binary (Go build)
  - Filters: `cmd/lab/**/*.go`, `cmd/lab/go.mod`, `cmd/lab/go.sum`
- `CliNix(source)` - Get lab binary (Nix build)
  - Filters: `cmd/lab/**/*`

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
# First run - builds everything
dagger call all --source=.

# Change a .nix file - only Nix checks run
echo "# comment" >> nix/hosts/common/default.nix
dagger call all --source=.  # Only LintNix + ValidateNix run

# Change Go code - only Go checks run
echo "// comment" >> cmd/lab/main.go
dagger call all --source=.  # Only LintGo + BuildCli + TestGo run
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
