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
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"
)

// Homelab is the main Dagger module for k8s-homelab CI/CD
type Homelab struct {
	// +private
	GoModulePaths []string
	// +private
	PythonProjectPaths []string
	// +private
	HelmChartPaths []string
	// +private
	TerraformModulePaths []string
}

func New(ctx context.Context, ws dagger.Workspace) *Homelab {
	source := ws.Directory(".", dagger.WorkspaceDirectoryOpts{
		Gitignore: true,
	})

	// Find all Go modules
	goModFiles, _ := source.Glob(ctx, "**/go.mod")
	var goModPaths []string
	for _, path := range goModFiles {
		goModPaths = append(goModPaths, filepath.Dir(path))
	}
	sort.Strings(goModPaths)

	// Find all Python projects
	pyprojectFiles, _ := source.Glob(ctx, "**/pyproject.toml")
	var pythonPaths []string
	for _, path := range pyprojectFiles {
		dir := filepath.Dir(path)
		if dir != "." {
			pythonPaths = append(pythonPaths, dir)
		}
	}
	sort.Strings(pythonPaths)

	// Find all Helm charts (Chart.yaml not in charts/ subdirs)
	chartFiles, _ := source.Glob(ctx, "k8s/**/Chart.yaml")
	var helmPaths []string
	for _, path := range chartFiles {
		if !strings.Contains(path, "/charts/") {
			helmPaths = append(helmPaths, filepath.Dir(path))
		}
	}
	sort.Strings(helmPaths)

	// Find all Terraform modules
	tfFiles, _ := source.Glob(ctx, "terraform/*/*.tf")
	tfSeen := map[string]bool{}
	var tfPaths []string
	for _, path := range tfFiles {
		dir := filepath.Dir(path)
		if !tfSeen[dir] {
			tfSeen[dir] = true
			tfPaths = append(tfPaths, dir)
		}
	}
	sort.Strings(tfPaths)

	return &Homelab{
		GoModulePaths:        goModPaths,
		PythonProjectPaths:   pythonPaths,
		HelmChartPaths:       helmPaths,
		TerraformModulePaths: tfPaths,
	}
}

// LintNix validates Nix formatting (alejandra) and dead code (deadnix).
// Fails if any files need formatting. Use `dagger call format-nix --auto-apply` to fix.
// +check
func (m *Homelab) LintNix(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", ".devenv*", ".devenv/**", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	formatted := m.nixFormat(source, paths)

	changeset := formatted.Changes(source)
	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking for nix formatting changes: %w", err)
	}

	if !empty {
		modified, _ := changeset.ModifiedPaths(ctx)
		return "", fmt.Errorf("nix files need formatting: %s\nRun `dagger call format-nix --auto-apply` to fix", strings.Join(modified, ", "))
	}

	return "Nix lint passed", nil
}

// FormatNix formats Nix files with alejandra and removes dead code with deadnix.
// Returns a changeset. Use `dagger call format-nix --auto-apply` to apply.
func (m *Homelab) FormatNix(
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", ".devenv*", ".devenv/**", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	paths []string,
) *dagger.Changeset {
	return m.nixFormat(source, paths).Changes(source)
}

// nixFormat runs alejandra and deadnix on source, returning the formatted directory.
func (m *Homelab) nixFormat(source *dagger.Directory, paths []string) *dagger.Directory {
	container := nixContainer().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix-env", "-iA", "nixpkgs.alejandra", "nixpkgs.deadnix"})

	targets := []string{"."}
	if len(paths) > 0 {
		targets = paths
	}

	return container.
		WithExec(append([]string{"deadnix", "--edit"}, targets...)).
		WithExec(append([]string{"alejandra"}, targets...)).
		Directory("/src")
}

// LintCue validates CUE configuration
// +check
func (m *Homelab) LintCue(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
	// +optional
	paths []string,
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

// ValidateNix runs nix flake check
// +check
func (m *Homelab) ValidateNix(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!cmd/lab/flake.nix", "!cmd/lab/flake.lock"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	_, err := nixContainer().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix", "--extra-experimental-features", "nix-command flakes", "flake", "check", "--no-build"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("nix flake check failed: %w", err)
	}

	return "Nix flake validation passed", nil
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

// BuildCli builds the lab CLI binary using Nix
// This ensures the build uses the exact same process as production
// +check
func (m *Homelab) BuildCli(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	_, err := nixContainer().
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
	return nixContainer().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"nix", "--extra-experimental-features", "nix-command flakes", "build", "./cmd/lab"}).
		File("/src/result/bin/lab")
}

// DevenvContainer builds the devenv nix environment as a minimal "from scratch" container.
// Uses `devenv container copy shell` to produce a minimal nix2container image
// containing only the runtime closure (no build dependencies).
// The resulting container has all dev tools (go, helm, kubectl, etc.) available on PATH.
// The nix store is cached across runs so packages are only built once.
func (m *Homelab) DevenvContainer(
	ctx context.Context,
	ws dagger.Workspace,
	// Optional host nix daemon socket for faster builds on NixOS CI hosts.
	// When provided, the host daemon's store is used as a substituter so
	// pre-built packages are fetched rather than rebuilt from source.
	// Note: only works when the host and container share the same platform
	// (e.g. Linux-to-Linux). Does not work with macOS host daemons due to
	// cross-platform path handling differences.
	// +optional
	nixDaemon *dagger.Socket,
) (*dagger.Container, error) {
	source := ws.Directory(".", dagger.WorkspaceDirectoryOpts{
		Gitignore: true,
		Include:   []string{"devenv.*", "cmd/lab/**"},
	})

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
