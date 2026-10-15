// Tests and formats every Python project in the repo, each in its own scoped
// container so that a change to one project leaves the others cached.
package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dagger/python/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// Python runs Python checks over every project in the repo.
type Python struct {
	// +private
	Container *dagger.Container
}

// Tests and formats every Python project in the repo.
func New(
	// Container to run in. It needs uv and black; defaults to the devenv ci
	// toolchain.
	// +optional
	container *dagger.Container,
) *Python {
	return &Python{Container: container}
}

func (m *Python) toolchain() *dagger.Container {
	if m.Container != nil {
		return m.Container
	}
	return dag.Devenv().Toolchain()
}

// blackCmd is the single definition of how black is invoked, so that the
// `dagger check` gate and `dagger generate python:format` cannot disagree on what
// formatted means — three hand-maintained copies of this list once did.
//
// black comes from the ci profile, so devenv.lock pins its version. It used to
// be `uv tool run black`, which resolves the newest PyPI release at run time
// while the pre-commit hook ran a Nix-pinned one. No --line-length on purpose:
// black's default is the one width that needs no agreement between call sites.
func blackCmd() []string {
	return []string{"black", "."}
}

// kubernetesTestlibPath is the library the releases' integration test projects
// share, relative to the repo root. They import it by relative path, so it has
// to sit where that path leads wherever one of them is run.
const kubernetesTestlibPath = "k8s/testlib"

// kubernetesTestsDir is where a release's integration tests live, relative to
// its chart directory.
const kubernetesTestsDir = "tests"

// pytestNoTestsExitCode is pytest's exit code for a run that collected nothing
// to execute. A project whose tests all deselect themselves — one holding only
// cluster tests, say — produces it, and that is a pass rather than a failure.
const pytestNoTestsExitCode = 5

// pythonSrcRoot is where a project's slice of the repo is laid out in a
// container: the project at its own Path beneath it, so that a relative path
// out of the project resolves as it does in a checkout.
const pythonSrcRoot = "/src"

// PythonProject is one Python project — a directory with a pyproject.toml —
// carrying only that project's files, so changing one project does not
// invalidate the BuildKit cache for the others.
type PythonProject struct {
	// Path is the project's directory relative to the repo root
	// (e.g. "k8s/foundation/kured/files/kured-webhook").
	Path string
	// Source is the project's scoped source directory.
	Source *dagger.Directory
	// Testlib is the shared integration test library, set only for the
	// projects that import it.
	Testlib *dagger.Directory
}

// usesTestlib reports whether the project at projPath is a release's
// integration test project: k8s/<tier>/<release>/tests.
func usesTestlib(projPath string) bool {
	parts := strings.Split(projPath, "/")
	return len(parts) == 4 && parts[0] == "k8s" && parts[3] == kubernetesTestsDir
}

// workdir is the project's directory in a container.
func (pp *PythonProject) workdir() string {
	return pythonSrcRoot + "/" + pp.Path
}

// usable reports why this project cannot be worked on, if it cannot. Source is
// populated by Projects(); a zero PythonProject reaching here means
// someone built one by hand or called a project function without a toolchain.
func (pp *PythonProject) usable(container *dagger.Container) error {
	if pp.Source == nil {
		return fmt.Errorf("PythonProject %s has no source directory; call Projects() first", pp.Path)
	}
	if container == nil {
		return fmt.Errorf("PythonProject %s: no toolchain container given", pp.Path)
	}
	return nil
}

// pythonProjects finds every project under source, narrowed to the ones
// containing `paths` when that is set. Each Source is a subdirectory of the
// +defaultPath source, so Directory IDs are stable across sessions.
func pythonProjects(ctx context.Context, source *dagger.Directory, paths []string) []*PythonProject {
	pyprojectFiles, _ := source.Glob(ctx, "**/pyproject.toml")
	var projectPaths []string
	for _, f := range pyprojectFiles {
		if dir := filepath.Dir(f); dir != "." {
			projectPaths = append(projectPaths, dir)
		}
	}
	sort.Strings(projectPaths)
	if len(paths) > 0 {
		projectPaths = matchProjectPaths(paths, projectPaths)
	}

	projects := make([]*PythonProject, len(projectPaths))
	for i, projPath := range projectPaths {
		projects[i] = &PythonProject{Path: projPath, Source: source.Directory(projPath)}
		if usesTestlib(projPath) {
			projects[i].Testlib = source.Directory(kubernetesTestlibPath)
		}
	}
	return projects
}

// Projects returns all discovered Python projects with scoped source
// directories.
func (m *Python) Projects(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
) []*PythonProject {
	return pythonProjects(ctx, source, nil)
}

// Test runs pytest for this Python project, in the given toolchain container.
//
// No -m here on purpose. Which markers an ordinary run excludes is a per-project
// decision, declared in each pyproject.toml's addopts — and it has to be,
// because a command-line -m replaces addopts rather than narrowing it. Passing
// one from here would re-enable the tests a project deselected for itself, and
// deselect the ones a project runs deliberately: jellyfin-exporter registers
// `integration` precisely so those tests run by default against its stub server.
func (pp *PythonProject) Test(ctx context.Context, container *dagger.Container) (string, error) {
	if err := pp.usable(container); err != nil {
		return "", err
	}

	// The library is mounted even though these runs deselect the tests that
	// use it: collecting them still imports their conftest.
	container = container.WithMountedDirectory(pp.workdir(), pp.Source)
	if pp.Testlib != nil {
		container = container.WithMountedDirectory(pythonSrcRoot+"/"+kubernetesTestlibPath, pp.Testlib)
	}
	_, err := container.
		WithWorkdir(pp.workdir()).
		WithExec([]string{"uv", "run", "--link-mode", "copy", "pytest"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			if execErr.ExitCode == pytestNoTestsExitCode {
				return fmt.Sprintf("Python tests deselected in %s (nothing left to run)", pp.Path), nil
			}
			return "", fmt.Errorf("pytest failed in %s:\n%s%s%w", pp.Path, execErr.Stdout, execErr.Stderr, err)
		}
		return "", fmt.Errorf("pytest failed in %s: %w", pp.Path, err)
	}
	return fmt.Sprintf("Python tests passed in %s", pp.Path), nil
}

// Format runs black on this project, returning the resulting changes.
func (pp *PythonProject) Format(ctx context.Context, container *dagger.Container) (*dagger.Changeset, error) {
	if err := pp.usable(container); err != nil {
		return nil, err
	}

	formatted, err := container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec(blackCmd()).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("black failed in %s:\n%s%w", pp.Path, execErr.Stderr, err)
		}
		return nil, fmt.Errorf("black failed in %s: %w", pp.Path, err)
	}

	// Re-rooted under pp.Path before diffing. Both sides are project-scoped, so
	// diffing them directly yields paths relative to the project, and
	// `--auto-apply` — which writes relative to the repo root — would drop the
	// file in the wrong place.
	before := dag.Directory().WithDirectory(pp.Path, pp.Source)
	after := dag.Directory().WithDirectory(pp.Path, formatted.Directory("/src"))

	return after.Changes(before), nil
}

// Format formats Python files with black across all discovered projects.
// Returns a changeset. Use `dagger generate python:format -y` to apply.
// +generate
func (m *Python) Format(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (*dagger.Changeset, error) {
	projects := pythonProjects(ctx, source, paths)
	if len(projects) == 0 {
		return dag.Changeset(), nil
	}
	container := m.toolchain()

	changesets := make([]*dagger.Changeset, len(projects))
	errs := make([]error, len(projects))

	var wg sync.WaitGroup
	for i, pp := range projects {
		wg.Go(func() {
			changesets[i], errs[i] = pp.Format(ctx, container)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return dag.Changeset().WithChangesets(changesets), nil
}

// Test runs pytest for all discovered Python projects.
//
// The release script tests under scripts/tests are left to release:test-scripts,
// which mounts the scripts and manifests they read.
// +check
func (m *Python) Test(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "scripts/tests/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	projects := pythonProjects(ctx, source, paths)
	if len(projects) == 0 {
		return "Python tests skipped (no projects found)", nil
	}
	container := m.toolchain()

	g := new(errgroup.Group)
	for _, pp := range projects {
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
