package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// blackCmd is the single definition of how black is invoked. Every call site
// uses it: lint, per-project format, and the aggregate format all have to agree
// with each other. Three hand-maintained copies of this argument list previously
// drifted apart, so the `dagger check` gate and `dagger call format-python`
// disagreed on line length.
//
// black comes from the ci profile, so its version is pinned by devenv.lock.
// This used to be `uv tool run black`, which resolves the newest release on
// PyPI at run time: the old black pre-commit hook in devenv.nix ran a
// Nix-pinned black while CI ran whatever had just been published, and nothing
// held the two to the same version. (The `black>=24.0.0` in the various
// pyproject.toml files is a dev dependency; `uv tool run` never read it.)
//
// No --line-length is passed on purpose — black's default is the one width that
// needs no agreement between call sites.
func blackCmd() []string {
	return []string{"black", "."}
}

// discoverPythonProjectPaths finds all Python project directories in source.
func discoverPythonProjectPaths(ctx context.Context, source *dagger.Directory) []string {
	pyprojectFiles, _ := source.Glob(ctx, "**/pyproject.toml")
	var paths []string
	for _, f := range pyprojectFiles {
		dir := filepath.Dir(f)
		if dir != "." {
			paths = append(paths, dir)
		}
	}
	sort.Strings(paths)
	return paths
}

// PythonProject is a Python project with a scoped source directory.
// Each PythonProject carries only the files for its project, enabling
// per-project caching: changing files in one project won't invalidate
// the cache for other projects.
type PythonProject struct {
	// Path is the project's directory relative to the repo root
	// (e.g. "k8s/foundation/kured/files/kured-webhook").
	Path string
	// Source is the project's scoped source directory.
	Source *dagger.Directory
}

// usable reports why this project cannot be worked on, if it cannot. Source is
// populated by PythonProjects(); a zero PythonProject reaching here means
// someone built one by hand or called a project function without a toolchain.
func (pp *PythonProject) usable(container *dagger.Container) error {
	if pp.Source == nil {
		return fmt.Errorf("PythonProject %s has no source directory; call PythonProjects() first", pp.Path)
	}
	if container == nil {
		return fmt.Errorf("PythonProject %s: no toolchain container given", pp.Path)
	}
	return nil
}

// PythonProjects returns all discovered Python projects with scoped source directories.
// Each project's Source is a subdirectory of the +defaultPath source, so
// Directory IDs are stable across sessions and cache independently.
func (m *Homelab) PythonProjects(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
) []*PythonProject {
	var projects []*PythonProject
	for _, projPath := range discoverPythonProjectPaths(ctx, source) {
		projects = append(projects, &PythonProject{
			Path:   projPath,
			Source: source.Directory(projPath),
		})
	}
	return projects
}

// Test runs pytest for this Python project, in the given toolchain container.
func (pp *PythonProject) Test(ctx context.Context, container *dagger.Container) (string, error) {
	if err := pp.usable(container); err != nil {
		return "", err
	}

	_, err := container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec([]string{"uv", "run", "--link-mode", "copy", "pytest", "-v"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("pytest failed in %s: %w", pp.Path, err)
	}
	return fmt.Sprintf("Python tests passed in %s", pp.Path), nil
}

// Lint runs black formatting check for this Python project.
func (pp *PythonProject) Lint(ctx context.Context, container *dagger.Container) (string, error) {
	if err := pp.usable(container); err != nil {
		return "", err
	}

	formatted := container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec(blackCmd()).
		Directory("/src")

	changeset := formatted.Changes(pp.Source)
	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking for python formatting changes in %s: %w", pp.Path, err)
	}

	if !empty {
		modified, _ := changeset.ModifiedPaths(ctx)
		return "", fmt.Errorf("python files need formatting in %s: %s\nRun `dagger call format-python --auto-apply` to fix",
			pp.Path, strings.Join(modified, ", "))
	}

	return fmt.Sprintf("Python lint passed in %s", pp.Path), nil
}

// Format formats Python files with black for this project, returning the formatted directory.
func (pp *PythonProject) Format(container *dagger.Container) *dagger.Directory {
	return container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec(blackCmd()).
		Directory("/src")
}

// LintPython runs Python formatting validation with black.
// Each project is linted with a scoped source directory so that changes
// in one project don't invalidate the BuildKit cache for other projects.
// Fails if any files need formatting. Use `dagger call format-python --auto-apply` to fix.
// +check
func (m *Homelab) LintPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	pythonProjectPaths := discoverPythonProjectPaths(ctx, source)
	if len(paths) > 0 {
		pythonProjectPaths = matchProjectPaths(paths, pythonProjectPaths)
	}
	if len(pythonProjectPaths) == 0 {
		return "Python lint skipped (no projects found)", nil
	}
	if container == nil {
		container = m.ciContainer()
	}

	g := new(errgroup.Group)
	for _, projPath := range pythonProjectPaths {
		pp := &PythonProject{
			Path:   projPath,
			Source: source.Directory(projPath),
		}
		g.Go(func() error {
			_, err := pp.Lint(ctx, container)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("python lint failed: %w", err)
	}

	return "Python lint passed", nil
}

// FormatPython formats Python files with black across all discovered projects.
// Returns a changeset. Use `dagger call format-python --auto-apply` to apply.
// +generate
func (m *Homelab) FormatPython(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) *dagger.Changeset {
	formatted := m.pythonFormat(ctx, source, paths, container)
	return formatted.Changes(source)
}

// pythonFormat runs black on all Python projects, returning the formatted directory.
func (m *Homelab) pythonFormat(ctx context.Context, source *dagger.Directory, paths []string, container *dagger.Container) *dagger.Directory {
	pythonProjectPaths := discoverPythonProjectPaths(ctx, source)
	if len(paths) > 0 {
		pythonProjectPaths = matchProjectPaths(paths, pythonProjectPaths)
	}
	if len(pythonProjectPaths) == 0 {
		return source
	}
	if container == nil {
		container = m.ciContainer()
	}
	container = container.WithMountedDirectory("/src", source)

	for _, dir := range pythonProjectPaths {
		container = container.
			WithWorkdir("/src/" + dir).
			WithExec(blackCmd())
	}

	return container.Directory("/src")
}

// TestPython runs pytest for all discovered Python projects.
// Each project is tested with a scoped source directory so that changes
// in one project don't invalidate the BuildKit cache for other projects.
// +check
func (m *Homelab) TestPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	pythonProjectPaths := discoverPythonProjectPaths(ctx, source)
	if len(paths) > 0 {
		pythonProjectPaths = matchProjectPaths(paths, pythonProjectPaths)
	}
	if len(pythonProjectPaths) == 0 {
		return "Python tests skipped (no projects found)", nil
	}
	if container == nil {
		container = m.ciContainer()
	}

	g := new(errgroup.Group)
	for _, projPath := range pythonProjectPaths {
		pp := &PythonProject{
			Path:   projPath,
			Source: source.Directory(projPath),
		}
		g.Go(func() error {
			_, err := pp.Test(ctx, container)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("pytest failed: %w", err)
	}

	return "Python tests passed", nil
}

// matchProjectPaths returns project paths that contain any of the given file paths.
func matchProjectPaths(filePaths, projectPaths []string) []string {
	matched := map[string]bool{}
	for _, p := range filePaths {
		for _, dir := range projectPaths {
			if strings.HasPrefix(p, dir+"/") {
				matched[dir] = true
			}
		}
	}

	var result []string
	for _, dir := range projectPaths {
		if matched[dir] {
			result = append(result, dir)
		}
	}
	return result
}
