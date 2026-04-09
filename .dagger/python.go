package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

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

// PythonProjects returns all discovered Python projects with scoped source directories.
// Each project's Source is a subdirectory of the +defaultPath source, so
// Directory IDs are stable across sessions and cache independently.
func (m *Homelab) PythonProjects(
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
) []*PythonProject {
	var projects []*PythonProject
	for _, projPath := range m.PythonProjectPaths {
		projects = append(projects, &PythonProject{
			Path:   projPath,
			Source: source.Directory(projPath),
		})
	}
	return projects
}

// Test runs pytest for this Python project.
// +check
func (pp *PythonProject) Test(ctx context.Context) (string, error) {
	if pp.Source == nil {
		return "", fmt.Errorf("PythonProject %s has no source directory; call PythonProjects() first", pp.Path)
	}

	_, err := dag.Container().
		From(uvImage).
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
// +check
func (pp *PythonProject) Lint(ctx context.Context) (string, error) {
	if pp.Source == nil {
		return "", fmt.Errorf("PythonProject %s has no source directory; call PythonProjects() first", pp.Path)
	}

	formatted := dag.Container().
		From(uvImage).
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec([]string{"uv", "tool", "run", "--link-mode", "copy", "black", "."}).
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
func (pp *PythonProject) Format() *dagger.Directory {
	return dag.Container().
		From(uvImage).
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec([]string{"uv", "tool", "run", "--link-mode", "copy", "black", "."}).
		Directory("/src")
}

// LintPython runs Python formatting validation with black.
// Each project is linted with a scoped source directory so that changes
// in one project don't invalidate the BuildKit cache for other projects.
// Fails if any files need formatting. Use `dagger call format-python --auto-apply` to fix.
// +check
func (m *Homelab) LintPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	projectPaths := m.PythonProjectPaths
	if len(paths) > 0 {
		projectPaths = matchProjectPaths(paths, projectPaths)
	}
	if len(projectPaths) == 0 {
		return "Python lint skipped (no projects found)", nil
	}

	g := new(errgroup.Group)
	for _, projPath := range projectPaths {
		pp := &PythonProject{
			Path:   projPath,
			Source: source.Directory(projPath),
		}
		g.Go(func() error {
			_, err := pp.Lint(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

	return "Python lint passed", nil
}

// FormatPython formats Python files with black across all discovered projects.
// Returns a changeset. Use `dagger call format-python --auto-apply` to apply.
func (m *Homelab) FormatPython(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (*dagger.Changeset, error) {
	formatted, err := m.pythonFormat(ctx, source, paths)
	if err != nil {
		return nil, err
	}
	return formatted.Changes(source), nil
}

// pythonFormat runs black on all Python projects, returning the formatted directory.
func (m *Homelab) pythonFormat(ctx context.Context, source *dagger.Directory, paths []string) (*dagger.Directory, error) {
	projectPaths := m.PythonProjectPaths
	if len(paths) > 0 {
		projectPaths = matchProjectPaths(paths, projectPaths)
	}
	if len(projectPaths) == 0 {
		return source, nil
	}

	container := dag.Container().
		From(uvImage).
		WithMountedDirectory("/src", source)

	for _, dir := range projectPaths {
		container = container.
			WithWorkdir("/src/" + dir).
			WithExec([]string{"uv", "tool", "run", "--link-mode", "copy", "black", "."})
	}

	return container.Directory("/src"), nil
}

// TestPython runs pytest for all discovered Python projects.
// Each project is tested with a scoped source directory so that changes
// in one project don't invalidate the BuildKit cache for other projects.
// +check
func (m *Homelab) TestPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	projectPaths := m.PythonProjectPaths
	if len(paths) > 0 {
		projectPaths = matchProjectPaths(paths, projectPaths)
	}
	if len(projectPaths) == 0 {
		return "Python tests skipped (no projects found)", nil
	}

	g := new(errgroup.Group)
	for _, projPath := range projectPaths {
		pp := &PythonProject{
			Path:   projPath,
			Source: source.Directory(projPath),
		}
		g.Go(func() error {
			_, err := pp.Test(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("pytest failed: %w", err)
	}

	return "Python tests passed", nil
}

// matchProjectPaths returns project paths that contain any of the given file paths.
func matchProjectPaths(filePaths []string, projectPaths []string) []string {
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
