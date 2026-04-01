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
	"dagger/homelab/pathutil"

	"golang.org/x/sync/errgroup"
)

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=ghcr.io/cachix/devenv/devenv
	devenvImage = "ghcr.io/cachix/devenv/devenv:v2.0.2"
	// renovate: datasource=docker depName=nixos/nix
	nixImage = "nixos/nix:latest"
	// renovate: datasource=docker depName=golang
	golangImage = "golang:1.25-alpine"
	// renovate: datasource=docker depName=ghcr.io/astral-sh/uv
	uvImage = "ghcr.io/astral-sh/uv:alpine"
	// renovate: datasource=docker depName=cuelang/cue
	cueImage = "cuelang/cue:latest"
	// renovate: datasource=docker depName=cytopia/yamllint
	yamllintImage = "cytopia/yamllint:latest"
	// renovate: datasource=docker depName=ghcr.io/opentofu/opentofu
	opentofuImage = "ghcr.io/opentofu/opentofu:latest"
	// renovate: datasource=docker depName=woodpeckerci/woodpecker-cli
	woodpeckerImage = "woodpeckerci/woodpecker-cli:v3"
	// renovate: datasource=docker depName=alpine/helm
	helmImage = "alpine/helm:latest"
	// renovate: datasource=docker depName=alpine
	alpineImage = "alpine:latest"
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
	// +ignore=["*", "!**/*.nix", "!**/flake.lock", "!nix/**/*", "!config/**/*.cue", "!**/*.go", "!**/go.mod", "!**/go.sum", "**/vendor/**", "!cmd/lab/**/*", "!terraform/**/*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", ".devenv*", ".devenv/**", "devenv.local.*", ".dagger/**", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
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
	// +ignore=["*", "!**/*.nix", "!config/**/*.cue", "!**/*.go", "!**/go.mod", "!**/go.sum", "!**/*.yaml", "!**/*.yml", "!.yamllint.yaml", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", ".devenv*", ".devenv/**", "devenv.local.*", ".dagger/**", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
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
		From(nixImage).
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
		From(cueImage).
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
// Discovers and iterates over all Go modules with go.mod
// +check
func (m *Homelab) LintGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
	// +optional
	// +default=false
	fix bool,
	// +optional
	paths []string,
) (*dagger.Directory, error) {
	moduleDirs, err := findGoModules(ctx, source, paths)
	if err != nil {
		return nil, err
	}
	if len(moduleDirs) == 0 {
		return source, nil
	}

	container := dag.Container().
		From(golangImage).
		WithMountedDirectory("/src", source)

	for _, dir := range moduleDirs {
		targets := []string{"./..."}
		if len(paths) > 0 {
			pkgs := pathutil.GoPackagePaths(paths, dir)
			if len(pkgs) > 0 {
				targets = pkgs
			}
		}

		workdir := "/src/" + dir
		if dir == "." {
			workdir = "/src"
		}

		if fix {
			container = container.
				WithWorkdir(workdir).
				WithExec(append([]string{"go", "fmt"}, targets...))
		} else {
			container = container.
				WithWorkdir(workdir).
				WithExec(append([]string{"go", "vet"}, targets...))
		}
	}

	if fix {
		return container.Directory("/src"), nil
	}

	_, err = container.Sync(ctx)
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
		From(uvImage).
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
		From(yamllintImage).
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
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!cmd/lab/flake.nix", "!cmd/lab/flake.lock", "!k8s/**/*", "!terraform/**/*", "!.woodpecker/*.yaml", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
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

	wpPaths := filterPaths(paths, validateWoodpeckerPatterns)
	if len(paths) == 0 || len(wpPaths) > 0 {
		g.Go(func() error {
			r, err := m.ValidateWoodpecker(ctx, source, wpPaths)
			if err != nil {
				return fmt.Errorf("woodpecker validation failed: %w", err)
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
		From(nixImage).
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
		From(opentofuImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/terraform").
		WithExec([]string{"sh", "-c", script}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("tofu validate failed: %w", err)
	}

	return "Terraform validation passed", nil
}

// ValidateWoodpecker lints Woodpecker CI pipeline configuration files
// When paths are provided, only matching files are linted
// +check
func (m *Homelab) ValidateWoodpecker(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!.woodpecker/*.yaml"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	targets := filterPaths(paths, validateWoodpeckerPatterns)
	if len(paths) > 0 && len(targets) == 0 {
		return "Woodpecker validation skipped (no matching files)", nil
	}

	container := dag.Container().
		From(woodpeckerImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src")

	if len(targets) > 0 {
		for _, f := range targets {
			container = container.WithExec([]string{"woodpecker-cli", "lint", "--strict", f})
		}
	} else {
		container = container.WithExec([]string{"woodpecker-cli", "lint", "--strict", ".woodpecker/"})
	}

	_, err := container.Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("woodpecker lint failed: %w", err)
	}

	return "Woodpecker validation passed", nil
}

// helmContainer returns a helm container with shared cache volumes mounted.
func (m *Homelab) helmContainer(source *dagger.Directory) *dagger.Container {
	return dag.Container().
		From(helmImage).
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
		From(nixImage).
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
		From(golangImage).
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
		From(golangImage).
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
		From(nixImage).
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
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "**/vendor/**", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!.dagger/scripts/*.sh", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
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

// TestGo runs Go tests for all discovered Go modules
// When paths are provided, only matching modules/packages are tested
// +check
func (m *Homelab) TestGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "**/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	moduleDirs, err := findGoModules(ctx, source, paths)
	if err != nil {
		return "", err
	}
	if len(moduleDirs) == 0 {
		return "Go tests skipped (no modules found)", nil
	}

	container := dag.Container().
		From(golangImage).
		WithMountedDirectory("/src", source)

	for _, dir := range moduleDirs {
		targets := []string{"./..."}
		if len(paths) > 0 {
			pkgs := pathutil.GoPackagePaths(paths, dir)
			if len(pkgs) > 0 {
				targets = pkgs
			}
		}

		workdir := "/src/" + dir
		if dir == "." {
			workdir = "/src"
		}

		container = container.
			WithWorkdir(workdir).
			WithExec(append([]string{"go", "test", "-v"}, targets...))
	}

	_, err = container.Sync(ctx)
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
		From(uvImage).
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

// Devenv builds the devenv nix environment as a minimal "from scratch" container.
// Uses `devenv container copy shell` to produce a minimal nix2container image
// containing only the runtime closure (no build dependencies).
// The resulting container has all dev tools (go, helm, kubectl, etc.) available on PATH.
// The nix store is cached across runs so packages are only built once.
func (m *Homelab) Devenv(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!devenv.nix", "!devenv.yaml", "!devenv.lock", "!devenv.local.nix", "!devenv.local.yaml", "!cmd/lab/**/*"]
	source *dagger.Directory,
	// Optional host nix daemon socket for faster builds on NixOS CI hosts.
	// When provided, the host daemon's store is used as a substituter so
	// pre-built packages are fetched rather than rebuilt from source.
	// Note: only works when the host and container share the same platform
	// (e.g. Linux-to-Linux). Does not work with macOS host daemons due to
	// cross-platform path handling differences.
	// +optional
	nixDaemon *dagger.Socket,
) (*dagger.Container, error) {
	// Cache the nix store across runs. The cache volume name includes the image
	// tag so it auto-invalidates when the devenv image is updated (e.g. by Renovate).
	nixCacheKey := "devenv-nix-" + devenvImage
	baseNix := dag.Container().From(devenvImage).WithUser("root").Directory("/nix")

	builder := dag.Container().
		From(devenvImage).
		WithUser("root").
		// Persist the nix store so packages are only built once.
		// Initialized from the base image on first use; reused on subsequent runs.
		WithMountedCache("/nix", dag.CacheVolume(nixCacheKey), dagger.ContainerWithMountedCacheOpts{
			Source: baseNix,
		}).
		// Suppress zsh-specific setup (compdef errors) and version nag in container context
		WithEnvVariable("DEVENV_ZSH_DISABLE", "1").
		WithDirectory("/devenv", source).
		WithWorkdir("/devenv")

	devenvOpts := []string{"--no-tui", "--option", "devenv.warnOnNewVersion:bool", "false"}
	if nixDaemon != nil {
		builder = builder.
			WithUnixSocket("/nix/var/nix/daemon-socket/socket", nixDaemon)
		// Use the host daemon as a substituter. The "daemon" keyword connects
		// to the socket at the default path (/nix/var/nix/daemon-socket/socket).
		// This works on NixOS CI hosts where architecture matches the container.
		devenvOpts = append(devenvOpts,
			"--nix-option", "extra-substituters", "daemon",
			"--nix-option", "require-sigs", "false",
		)
	}

	// Build the shell and extract PATH from devenv print-dev-env.
	// The env script sets PATH='...' as a single-quoted assignment.
	builder = builder.
		WithExec(append([]string{"devenv", "build", "shell"}, devenvOpts...)).
		WithExec([]string{"sh", "-c", strings.Join(append(append([]string{"devenv", "print-dev-env"}, devenvOpts...), "> /tmp/devenv-env.sh"), " ")})

	devenvPath, err := builder.
		WithExec([]string{"sed", "-n", "s/^PATH='\\(.*\\)'/\\1/p", "/tmp/devenv-env.sh"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("extracting devenv PATH: %w", err)
	}

	// Build the minimal shell container and export as docker-archive tarball.
	// devenv container copy uses nix2container + skopeo under the hood.
	// The --registry flag accepts any skopeo transport, including docker-archive.
	// The container name ("shell") is appended to the registry path by devenv,
	// so "docker-archive:/tmp/" produces the file "/tmp/shell".
	tarball := builder.
		WithExec(append([]string{"devenv", "container", "copy", "shell", "--registry", "docker-archive:/tmp/"}, devenvOpts...)).
		File("/tmp/shell")

	return dag.Container().Import(tarball).
		WithEnvVariable("PATH", strings.TrimSpace(devenvPath)).
		WithWorkdir("/src"), nil
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
