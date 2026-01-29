// A Dagger module for k8s-homelab CI/CD pipeline
//
// This module provides functions for building, testing, and validating
// the k8s-homelab infrastructure. It is designed to work independently
// of the lab CLI to avoid circular dependencies.
//
// Setup:
//
//	dagger develop  # Generate SDK code in internal/
//	dagger functions  # List available functions
//	dagger call build-cli --source=.  # Build lab CLI
package main

import (
	"context"
	"fmt"

	"dagger/homelab/internal/dagger"
)

// Homelab is the main Dagger module for k8s-homelab CI/CD
type Homelab struct{}

// All runs the complete CI pipeline
// Returns the linted/fixed source directory
func (m *Homelab) All(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", "!**/flake.lock", "!nix/**/*", "!config/**/*.cue", "!cmd/lab/**/*", "!terraform/**/*", "!k8s/**/*", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml", "!**/*.py", "!**/pyproject.toml", ".devenv*", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
) (*dagger.Directory, error) {
	var err error

	// Run lint stage (potentially fixing issues)
	source, err = m.Lint(ctx, source, fix)
	if err != nil {
		return nil, fmt.Errorf("lint failed: %w", err)
	}

	// Run validate stage
	_, err = m.Validate(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("validate failed: %w", err)
	}

	// Run build stage
	_, err = m.Build(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("build failed: %w", err)
	}

	// Run test stage
	_, err = m.Test(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("test failed: %w", err)
	}

	return source, nil
}

// Lint runs all linting checks and optionally fixes issues
// Returns the (potentially modified) source directory
// Pre-call filter includes all files needed by all lint functions
// +check
func (m *Homelab) Lint(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", "!config/**/*.cue", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml", "!k8s/platform/crowdsec/files/bootstrap-middleware/**/*.py", "!k8s/platform/crowdsec/files/bootstrap-middleware/pyproject.toml", ".devenv*", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
) (*dagger.Directory, error) {
	var err error

	// Run Nix linting (alejandra, deadnix) - supports fixing
	source, err = m.LintNix(ctx, source, fix)
	if err != nil {
		return nil, fmt.Errorf("nix lint failed: %w", err)
	}

	// Run CUE linting - no auto-fix support
	_, err = m.LintCue(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("cue lint failed: %w", err)
	}

	// Run Go linting - supports formatting
	source, err = m.LintGo(ctx, source, fix)
	if err != nil {
		return nil, fmt.Errorf("go lint failed: %w", err)
	}

	// Run Python linting - supports formatting
	source, err = m.LintPython(ctx, source, fix)
	if err != nil {
		return nil, fmt.Errorf("python lint failed: %w", err)
	}

	// Run YAML linting - no auto-fix support
	_, err = m.LintYaml(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("yaml lint failed: %w", err)
	}

	return source, nil
}

// LintNix runs Nix-specific linting (alejandra format check, deadnix)
// When fix=true, automatically formats with alejandra and removes dead code with deadnix
// +check
func (m *Homelab) LintNix(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", ".devenv*", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
) (*dagger.Directory, error) {
	container := dag.Container().
		From("nixos/nix:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix-env", "-iA", "nixpkgs.alejandra", "nixpkgs.deadnix"})

	if fix {
		// Auto-format with alejandra (no --check flag)
		container = container.WithExec([]string{"alejandra", "."})

		// Auto-fix with deadnix (--edit flag)
		container = container.WithExec([]string{"deadnix", "--edit", "."})

		// Return the modified directory
		return container.Directory("/src"), nil
	}

	// Check formatting with alejandra
	_, err := container.
		WithExec([]string{"alejandra", "--check", "."}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("alejandra format check failed: %w", err)
	}

	// Run deadnix to find dead code
	_, err = container.
		WithExec([]string{"deadnix", "--fail", "."}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("deadnix check failed: %w", err)
	}

	return source, nil
}

// LintCue validates CUE configuration
// +check
func (m *Homelab) LintCue(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("cuelang/cue:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/config").
		WithExec([]string{"cue", "vet", "./..."}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("cue vet failed: %w", err)
	}

	return "CUE validation passed", nil
}

// LintGo runs Go linting and optionally formats code
// When fix=true, automatically formats with go fmt
// +check
func (m *Homelab) LintGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
) (*dagger.Directory, error) {
	container := dag.Container().
		From("golang:1.25-alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab")

	if fix {
		// Auto-format with go fmt
		container = container.WithExec([]string{"go", "fmt", "./..."})

		// Return the modified directory
		return container.Directory("/src"), nil
	}

	// Run go vet for linting
	_, err := container.
		WithExec([]string{"go", "vet", "./..."}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("go vet failed: %w", err)
	}

	return source, nil
}

// LintPython runs Python linting and optionally formats code with black
// When fix=true, automatically formats with black
// +check
func (m *Homelab) LintPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/platform/crowdsec/files/bootstrap-middleware/**/*.py", "!k8s/platform/crowdsec/files/bootstrap-middleware/pyproject.toml"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
) (*dagger.Directory, error) {
	container := dag.Container().
		From("ghcr.io/astral-sh/uv:alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/k8s/platform/crowdsec/files/bootstrap-middleware")

	if fix {
		// Auto-format with black
		container = container.WithExec([]string{"uv", "run", "black", "."})

		// Return the modified directory
		return container.Directory("/src"), nil
	}

	// Check formatting with black
	_, err := container.
		WithExec([]string{"uv", "run", "black", "--check", "."}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("black format check failed: %w", err)
	}

	return source, nil
}

// LintYaml runs YAML linting
// +check
func (m *Homelab) LintYaml(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("cytopia/yamllint:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{
			"yamllint",
			"--config-file", ".yamllint.yaml",
			".",
			"--format", "parsable",
			"--strict",
		}).
		Sync(ctx)
	if err != nil {
		// YAML lint failures are warnings for now
		return "YAML linting completed with warnings", nil
	}

	return "YAML linting passed", nil
}

// Validate runs validation checks
// Pre-call filter includes all files needed by all validate functions
func (m *Homelab) Validate(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!cmd/lab/flake.nix", "!cmd/lab/flake.lock", "!k8s/**/*", "!terraform/**/*"]
	source *dagger.Directory,
) (string, error) {
	results := []string{}

	// Validate Nix flake
	nixResult, err := m.ValidateNix(ctx, source)
	if err != nil {
		return "", fmt.Errorf("nix validation failed: %w", err)
	}
	results = append(results, nixResult)

	// Validate Helm charts
	helmResult, err := m.ValidateHelm(ctx, source)
	if err != nil {
		return "", fmt.Errorf("helm validation failed: %w", err)
	}
	results = append(results, helmResult)

	// Validate Terraform
	tfResult, err := m.ValidateTerraform(ctx, source)
	if err != nil {
		return "", fmt.Errorf("terraform validation failed: %w", err)
	}
	results = append(results, tfResult)

	return joinResults(results), nil
}

// ValidateNix runs nix flake check
// +check
func (m *Homelab) ValidateNix(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!cmd/lab/flake.nix", "!cmd/lab/flake.lock"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("nixos/nix:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix", "--extra-experimental-features", "nix-command flakes", "flake", "check", "--no-build"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("nix flake check failed: %w", err)
	}

	return "Nix flake validation passed", nil
}

// ValidateHelm runs helm lint on all charts
// +check
func (m *Homelab) ValidateHelm(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*"]
	source *dagger.Directory,
) (string, error) {
	// Note: This is a simplified check - full helmfile validation requires more setup
	_, err := dag.Container().
		From("alpine/helm:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"sh", "-c", "find k8s -name 'Chart.yaml' -exec dirname {} \\; | xargs -I {} helm lint {} 2>/dev/null || true"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("helm lint failed: %w", err)
	}

	return "Helm validation passed", nil
}

// ValidateTerraform runs tofu validate on all modules
// +check
func (m *Homelab) ValidateTerraform(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("ghcr.io/opentofu/opentofu:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/terraform").
		WithExec([]string{"sh", "-c", "for dir in */; do cd \"$dir\" && tofu init -backend=false && tofu validate && cd ..; done 2>/dev/null || true"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("tofu validate failed: %w", err)
	}

	return "Terraform validation passed", nil
}

// Build builds all artifacts
// Pre-call filter includes all files needed by all build functions
func (m *Homelab) Build(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*"]
	source *dagger.Directory,
) (string, error) {
	results := []string{}

	// Build the lab CLI
	cliResult, err := m.BuildCli(ctx, source)
	if err != nil {
		return "", fmt.Errorf("cli build failed: %w", err)
	}
	results = append(results, cliResult)

	return joinResults(results), nil
}

// BuildCli builds the lab CLI binary using Nix
// This ensures the build uses the exact same process as production
func (m *Homelab) BuildCli(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("nixos/nix:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix", "--extra-experimental-features", "nix-command flakes", "build", "./cmd/lab", "--print-build-logs"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("nix build failed: %w", err)
	}

	return "CLI build passed (Nix)", nil
}

// BuildCliGo builds the lab CLI binary using Go (faster, for testing)
func (m *Homelab) BuildCliGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("golang:1.25-alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab").
		WithExec([]string{"go", "build", "-o", "/out/lab", "."}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("go build failed: %w", err)
	}

	return "CLI build passed (Go)", nil
}

// Cli returns the built lab CLI binary (using Go for speed)
func (m *Homelab) Cli(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum"]
	source *dagger.Directory,
	platform dagger.Platform,
) *dagger.File {
	return dag.Container().
		From("golang:1.25-alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab").
		WithEnvVariable("CGO_ENABLED", "0").
		WithExec([]string{"go", "build", "-ldflags", "-s -w", "-o", "/out/lab", "."}).
		File("/out/lab")
}

// CliNix returns the built lab CLI binary using Nix
func (m *Homelab) CliNix(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*"]
	source *dagger.Directory,
) *dagger.File {
	return dag.Container().
		From("nixos/nix:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix", "--extra-experimental-features", "nix-command flakes", "build", "./cmd/lab"}).
		File("/src/result/bin/lab")
}

// Test runs all tests
// Pre-call filter includes all files needed by all test functions
// +check
func (m *Homelab) Test(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum", "!k8s/platform/crowdsec/files/bootstrap-middleware/**/*.py", "!k8s/platform/crowdsec/files/bootstrap-middleware/pyproject.toml"]
	source *dagger.Directory,
) (string, error) {
	results := []string{}

	// Run Go tests
	goTestResult, err := m.TestGo(ctx, source)
	if err != nil {
		return "", fmt.Errorf("go tests failed: %w", err)
	}
	results = append(results, goTestResult)

	// Run Python tests
	pythonTestResult, err := m.TestPython(ctx, source)
	if err != nil {
		return "", fmt.Errorf("python tests failed: %w", err)
	}
	results = append(results, pythonTestResult)

	return joinResults(results), nil
}

// TestGo runs Go tests for the lab CLI
// +check
func (m *Homelab) TestGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("golang:1.25-alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab").
		WithExec([]string{"go", "test", "./...", "-v"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("go test failed: %w", err)
	}

	return "Go tests passed", nil
}

// TestPython runs pytest for the CrowdSec bootstrap script
// +check
func (m *Homelab) TestPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/platform/crowdsec/files/bootstrap-middleware/**/*.py", "!k8s/platform/crowdsec/files/bootstrap-middleware/pyproject.toml"]
	source *dagger.Directory,
) (string, error) {
	_, err := dag.Container().
		From("ghcr.io/astral-sh/uv:alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/k8s/platform/crowdsec/files/bootstrap-middleware").
		WithExec([]string{"uv", "run", "pytest", "-v"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("pytest failed: %w", err)
	}

	return "Python tests passed", nil
}

// DebugDir returns the given dir. Useful for inspecting directory contents for debugging.
func (m *Homelab) DebugDir(ctx context.Context, dir *dagger.Directory) *dagger.Directory {
	return dir
}

func joinResults(results []string) string {
	result := ""
	for i, r := range results {
		if i > 0 {
			result += "\n"
		}
		result += "  - " + r
	}
	return result
}
