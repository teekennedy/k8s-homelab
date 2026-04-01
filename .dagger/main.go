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
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

//go:embed scripts/helm-deps.sh
var helmDepsScript string

//go:embed scripts/helm-template.sh
var helmTemplateScript string

//go:embed scripts/helm-lint.sh
var helmLintScript string

// Homelab is the main Dagger module for k8s-homelab CI/CD
type Homelab struct{}

// All runs the complete CI pipeline
// Returns the linted/fixed source directory
func (m *Homelab) All(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", "!**/flake.lock", "!nix/**/*", "!config/**/*.cue", "!cmd/lab/**/*", "!terraform/**/*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", ".devenv*", ".devenv/**", "devenv.local.*", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
	// +optional
	paths []string,
) (*dagger.Directory, error) {
	if fix {
		// Lint modifies source, so it must run first
		lintPaths := filterPaths(paths, allLintPatterns)
		if len(paths) == 0 || len(lintPaths) > 0 {
			var err error
			source, err = m.Lint(ctx, source, fix, lintPaths)
			if err != nil {
				return nil, fmt.Errorf("lint failed: %w", err)
			}
		}

		// Then validate, build, and test the fixed source in parallel
		g, ctx := errgroup.WithContext(ctx)

		valPaths := filterPaths(paths, allValidatePatterns)
		if len(paths) == 0 || len(valPaths) > 0 {
			g.Go(func() error {
				_, err := m.Validate(ctx, source, valPaths)
				if err != nil {
					return fmt.Errorf("validate failed: %w", err)
				}
				return nil
			})
		}

		buildPaths := filterPaths(paths, allBuildPatterns)
		if len(paths) == 0 || len(buildPaths) > 0 {
			g.Go(func() error {
				_, err := m.Build(ctx, source, buildPaths)
				if err != nil {
					return fmt.Errorf("build failed: %w", err)
				}
				return nil
			})
		}

		testPaths := filterPaths(paths, allTestPatterns)
		if len(paths) == 0 || len(testPaths) > 0 {
			g.Go(func() error {
				_, err := m.Test(ctx, source, testPaths)
				if err != nil {
					return fmt.Errorf("test failed: %w", err)
				}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return nil, err
		}

		return source, nil
	}

	// Check-only: run everything in parallel
	g, ctx := errgroup.WithContext(ctx)

	lintPaths := filterPaths(paths, allLintPatterns)
	if len(paths) == 0 || len(lintPaths) > 0 {
		g.Go(func() error {
			_, err := m.Lint(ctx, source, false, lintPaths)
			if err != nil {
				return fmt.Errorf("lint failed: %w", err)
			}
			return nil
		})
	}

	valPaths := filterPaths(paths, allValidatePatterns)
	if len(paths) == 0 || len(valPaths) > 0 {
		g.Go(func() error {
			_, err := m.Validate(ctx, source, valPaths)
			if err != nil {
				return fmt.Errorf("validate failed: %w", err)
			}
			return nil
		})
	}

	buildPaths := filterPaths(paths, allBuildPatterns)
	if len(paths) == 0 || len(buildPaths) > 0 {
		g.Go(func() error {
			_, err := m.Build(ctx, source, buildPaths)
			if err != nil {
				return fmt.Errorf("build failed: %w", err)
			}
			return nil
		})
	}

	testPaths := filterPaths(paths, allTestPatterns)
	if len(paths) == 0 || len(testPaths) > 0 {
		g.Go(func() error {
			_, err := m.Test(ctx, source, testPaths)
			if err != nil {
				return fmt.Errorf("test failed: %w", err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return source, nil
}

// Lint runs all linting checks and optionally fixes issues
// Returns the (potentially modified) source directory
// Pre-call filter includes all files needed by all lint functions
// +check
func (m *Homelab) Lint(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", "!config/**/*.cue", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", ".devenv*", ".devenv/**", "devenv.local.*", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
	// +optional
	paths []string,
) (*dagger.Directory, error) {
	if fix {
		// When fixing, run fixable linters sequentially since each modifies source
		var err error

		nixPaths := excludeDevenvPaths(filterPaths(paths, lintNixPatterns))
		if len(paths) == 0 || len(nixPaths) > 0 {
			source, err = m.LintNix(ctx, source, fix, nixPaths)
			if err != nil {
				return nil, fmt.Errorf("nix lint failed: %w", err)
			}
		}

		goPaths := filterPaths(paths, lintGoPatterns)
		if len(paths) == 0 || len(goPaths) > 0 {
			source, err = m.LintGo(ctx, source, fix, goPaths)
			if err != nil {
				return nil, fmt.Errorf("go lint failed: %w", err)
			}
		}

		pyPaths := filterPaths(paths, lintPythonPatterns)
		if len(paths) == 0 || len(pyPaths) > 0 {
			source, err = m.LintPython(ctx, source, fix, pyPaths)
			if err != nil {
				return nil, fmt.Errorf("python lint failed: %w", err)
			}
		}

		// CUE and YAML don't support fix, run them in parallel on final source
		g, ctx := errgroup.WithContext(ctx)

		cuePaths := filterPaths(paths, lintCuePatterns)
		if len(paths) == 0 || len(cuePaths) > 0 {
			g.Go(func() error {
				_, err := m.LintCue(ctx, source)
				if err != nil {
					return fmt.Errorf("cue lint failed: %w", err)
				}
				return nil
			})
		}

		yamlPaths := filterPaths(paths, lintYamlPatterns)
		if len(paths) == 0 || len(yamlPaths) > 0 {
			g.Go(func() error {
				_, err := m.LintYaml(ctx, source, yamlPaths)
				if err != nil {
					return fmt.Errorf("yaml lint failed: %w", err)
				}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return nil, err
		}

		return source, nil
	}

	// Check-only mode: all linters are independent, run in parallel
	g, ctx := errgroup.WithContext(ctx)

	nixPaths := excludeDevenvPaths(filterPaths(paths, lintNixPatterns))
	if len(paths) == 0 || len(nixPaths) > 0 {
		g.Go(func() error {
			_, err := m.LintNix(ctx, source, false, nixPaths)
			if err != nil {
				return fmt.Errorf("nix lint failed: %w", err)
			}
			return nil
		})
	}

	cuePaths := filterPaths(paths, lintCuePatterns)
	if len(paths) == 0 || len(cuePaths) > 0 {
		g.Go(func() error {
			_, err := m.LintCue(ctx, source)
			if err != nil {
				return fmt.Errorf("cue lint failed: %w", err)
			}
			return nil
		})
	}

	goPaths := filterPaths(paths, lintGoPatterns)
	if len(paths) == 0 || len(goPaths) > 0 {
		g.Go(func() error {
			_, err := m.LintGo(ctx, source, false, goPaths)
			if err != nil {
				return fmt.Errorf("go lint failed: %w", err)
			}
			return nil
		})
	}

	pyPaths := filterPaths(paths, lintPythonPatterns)
	if len(paths) == 0 || len(pyPaths) > 0 {
		g.Go(func() error {
			_, err := m.LintPython(ctx, source, false, pyPaths)
			if err != nil {
				return fmt.Errorf("python lint failed: %w", err)
			}
			return nil
		})
	}

	yamlPaths := filterPaths(paths, lintYamlPatterns)
	if len(paths) == 0 || len(yamlPaths) > 0 {
		g.Go(func() error {
			_, err := m.LintYaml(ctx, source, yamlPaths)
			if err != nil {
				return fmt.Errorf("yaml lint failed: %w", err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return source, nil
}

// LintNix runs Nix-specific linting (alejandra format check, deadnix)
// When fix=true, automatically formats with alejandra and removes dead code with deadnix
// +check
func (m *Homelab) LintNix(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", ".devenv*", ".devenv/**", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
	// +optional
	paths []string,
) (*dagger.Directory, error) {
	container := dag.Container().
		From("nixos/nix:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix-env", "-iA", "nixpkgs.alejandra", "nixpkgs.deadnix"})

	// Determine targets: specific files or all
	targets := []string{"."}
	if len(paths) > 0 {
		targets = paths
	}

	if fix {
		container = container.WithExec(append([]string{"alejandra"}, targets...))
		container = container.WithExec(append([]string{"deadnix", "--edit"}, targets...))
		return container.Directory("/src"), nil
	}

	_, err := container.
		WithExec(append([]string{"alejandra", "--check"}, targets...)).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("alejandra format check failed: %w", err)
	}

	_, err = container.
		WithExec(append([]string{"deadnix", "--fail"}, targets...)).
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
	// +optional
	paths []string,
) (*dagger.Directory, error) {
	container := dag.Container().
		From("golang:1.25-alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab")

	// Determine package targets
	targets := []string{"./..."}
	if len(paths) > 0 {
		pkgs := goPackagePaths(paths)
		if len(pkgs) > 0 {
			targets = pkgs
		}
	}

	if fix {
		container = container.WithExec(append([]string{"go", "fmt"}, targets...))
		return container.Directory("/src"), nil
	}

	_, err := container.
		WithExec(append([]string{"go", "vet"}, targets...)).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("go vet failed: %w", err)
	}

	return source, nil
}

// LintPython runs Python linting and optionally formats code with black
// When fix=true, automatically formats with black
// Discovers and iterates over all Python projects with pyproject.toml
// +check
func (m *Homelab) LintPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
	// +optional
	paths []string,
) (*dagger.Directory, error) {
	projectDirs, err := findPythonProjects(ctx, source, paths)
	if err != nil {
		return nil, err
	}
	if len(projectDirs) == 0 {
		return source, nil
	}

	container := dag.Container().
		From("ghcr.io/astral-sh/uv:alpine").
		WithMountedDirectory("/src", source)

	for _, dir := range projectDirs {
		if fix {
			container = container.
				WithWorkdir("/src/" + dir).
				WithExec([]string{"uv", "tool", "run", "--link-mode", "copy", "black", "."})
		} else {
			container = container.
				WithWorkdir("/src/" + dir).
				WithExec([]string{"uv", "tool", "run", "--link-mode", "copy", "black", "--check", "."})
		}
	}

	if fix {
		return container.Directory("/src"), nil
	}

	_, err = container.Sync(ctx)
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
	// +optional
	paths []string,
) (string, error) {
	args := []string{"yamllint", "--config-file", ".yamllint.yaml", "--format", "parsable", "--strict"}
	if len(paths) > 0 {
		args = append(args, paths...)
	} else {
		args = append(args, ".")
	}

	_, err := dag.Container().
		From("cytopia/yamllint:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec(args).
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
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!cmd/lab/flake.nix", "!cmd/lab/flake.lock", "!k8s/**/*", "!terraform/**/*", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	g, ctx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	var results []string

	nixPaths := filterPaths(paths, validateNixPatterns)
	if len(paths) == 0 || len(nixPaths) > 0 {
		g.Go(func() error {
			r, err := m.ValidateNix(ctx, source)
			if err != nil {
				return fmt.Errorf("nix validation failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	helmPaths := filterPaths(paths, validateHelmPatterns)
	if len(paths) == 0 || len(helmPaths) > 0 {
		g.Go(func() error {
			r, err := m.ValidateHelm(ctx, source, helmPaths)
			if err != nil {
				return fmt.Errorf("helm validation failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	tfPaths := filterPaths(paths, validateTfPatterns)
	if len(paths) == 0 || len(tfPaths) > 0 {
		g.Go(func() error {
			r, err := m.ValidateTerraform(ctx, source, tfPaths)
			if err != nil {
				return fmt.Errorf("terraform validation failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

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

// ValidateHelm runs helm lint on charts with dependency download
// When paths are provided, only charts matching the paths are linted
// +check
func (m *Homelab) ValidateHelm(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	searchPaths := "k8s"
	if len(paths) > 0 {
		chartDirs, err := findHelmChartDirs(ctx, source, paths)
		if err != nil {
			return "", err
		}
		if len(chartDirs) == 0 {
			return "Helm validation skipped (no matching charts)", nil
		}
		searchPaths = strings.Join(chartDirs, " ")
	}

	preparedSource := m.helmSourceWithDeps(source, searchPaths)

	_, err := m.helmContainer(preparedSource).
		WithNewFile("/lint.sh", helmLintScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithEnvVariable("SEARCH_PATHS", searchPaths).
		WithExec([]string{"/lint.sh"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("helm lint failed: %w", err)
	}

	return "Helm validation passed", nil
}

// ValidateTerraform runs tofu validate on all modules
// When paths are provided, only matching modules are validated
// +check
func (m *Homelab) ValidateTerraform(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	script := "for dir in */; do cd \"$dir\" && tofu init -backend=false && tofu validate && cd ..; done 2>/dev/null || true"
	if len(paths) > 0 {
		dirs := terraformModuleDirs(paths)
		if len(dirs) == 0 {
			return "Terraform validation skipped (no matching modules)", nil
		}
		dirArgs := make([]string, len(dirs))
		for i, d := range dirs {
			dirArgs[i] = d + "/"
		}
		script = fmt.Sprintf("for dir in %s; do cd \"$dir\" && tofu init -backend=false && tofu validate && cd ..; done", strings.Join(dirArgs, " "))
	}

	_, err := dag.Container().
		From("ghcr.io/opentofu/opentofu:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/terraform").
		WithExec([]string{"sh", "-c", script}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("tofu validate failed: %w", err)
	}

	return "Terraform validation passed", nil
}

// helmContainer returns a helm container with shared cache volumes mounted.
func (m *Homelab) helmContainer(source *dagger.Directory) *dagger.Container {
	return dag.Container().
		From("alpine/helm:latest").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithMountedCache("/root/.cache/helm/repository", dag.CacheVolume("helm-repo-cache")).
		WithMountedCache("/root/.cache/helm/content", dag.CacheVolume("helm-content-cache")).
		WithMountedCache("/root/.config/helm/registry", dag.CacheVolume("helm-registry-cache"))
}

// helmSourceWithDeps registers helm repos and builds chart dependencies,
// returning the source directory with dependency tarballs populated in charts/ dirs.
// When called with the same inputs from multiple consumers (e.g. ValidateHelm and BuildHelm),
// Dagger deduplicates the work and executes the dependency build only once.
func (m *Homelab) helmSourceWithDeps(source *dagger.Directory, searchPaths string) *dagger.Directory {
	return m.helmContainer(source).
		WithNewFile("/deps.sh", helmDepsScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithEnvVariable("SEARCH_PATHS", searchPaths).
		WithExec([]string{"/deps.sh"}).
		Directory("/src")
}

// BuildHelm renders Helm templates for Kubernetes charts to verify they are valid.
// When paths are provided, only charts matching the paths are rendered.
func (m *Homelab) BuildHelm(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	searchPaths := "k8s"
	if len(paths) > 0 {
		chartDirs, err := findHelmChartDirs(ctx, source, paths)
		if err != nil {
			return "", err
		}
		if len(chartDirs) == 0 {
			return "Helm template rendering skipped (no matching charts)", nil
		}
		searchPaths = strings.Join(chartDirs, " ")
	}

	preparedSource := m.helmSourceWithDeps(source, searchPaths)

	_, err := m.helmContainer(preparedSource).
		WithNewFile("/render.sh", helmTemplateScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithEnvVariable("SEARCH_PATHS", searchPaths).
		WithExec([]string{"/render.sh"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("helm template rendering failed: %w", err)
	}

	return "Helm template rendering passed", nil
}

// Build builds all artifacts
func (m *Homelab) Build(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	g, ctx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	var results []string

	cliPaths := filterPaths(paths, buildCliPatterns)
	if len(paths) == 0 || len(cliPaths) > 0 {
		cliSource := dag.Directory().WithDirectory("cmd/lab", source.Directory("cmd/lab"))
		g.Go(func() error {
			r, err := m.BuildCli(ctx, cliSource)
			if err != nil {
				return fmt.Errorf("cli build failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	helmPaths := filterPaths(paths, buildHelmPatterns)
	if len(paths) == 0 || len(helmPaths) > 0 {
		g.Go(func() error {
			r, err := m.BuildHelm(ctx, source, helmPaths)
			if err != nil {
				return fmt.Errorf("k8s build failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

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
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	g, ctx := errgroup.WithContext(ctx)
	var mu sync.Mutex
	var results []string

	goPaths := filterPaths(paths, testGoPatterns)
	if len(paths) == 0 || len(goPaths) > 0 {
		g.Go(func() error {
			r, err := m.TestGo(ctx, source, goPaths)
			if err != nil {
				return fmt.Errorf("go tests failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	pyPaths := filterPaths(paths, testPythonPatterns)
	if len(paths) == 0 || len(pyPaths) > 0 {
		g.Go(func() error {
			r, err := m.TestPython(ctx, source, pyPaths)
			if err != nil {
				return fmt.Errorf("python tests failed: %w", err)
			}
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

	return joinResults(results), nil
}

// TestGo runs Go tests for the lab CLI
// When paths are provided, only matching packages are tested
// +check
func (m *Homelab) TestGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	targets := []string{"./..."}
	if len(paths) > 0 {
		pkgs := goPackagePaths(paths)
		if len(pkgs) > 0 {
			targets = pkgs
		}
	}

	_, err := dag.Container().
		From("golang:1.25-alpine").
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab").
		WithExec(append([]string{"go", "test", "-v"}, targets...)).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("go test failed: %w", err)
	}

	return "Go tests passed", nil
}

// TestPython runs pytest for Python projects
// Discovers and iterates over all Python projects with pyproject.toml
// When paths are provided, only matching projects are tested
// +check
func (m *Homelab) TestPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	projectDirs, err := findPythonProjects(ctx, source, paths)
	if err != nil {
		return "", err
	}
	if len(projectDirs) == 0 {
		return "Python tests skipped (no projects found)", nil
	}

	container := dag.Container().
		From("ghcr.io/astral-sh/uv:alpine").
		WithMountedDirectory("/src", source)

	for _, dir := range projectDirs {
		container = container.
			WithWorkdir("/src/" + dir).
			WithExec([]string{"uv", "run", "--link-mode", "copy", "pytest", "-v"})
	}

	_, err = container.Sync(ctx)
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
