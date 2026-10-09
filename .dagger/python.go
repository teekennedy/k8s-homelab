package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// blackCmd is the single definition of how black is invoked, so that the
// `dagger check` gate and `dagger call format-python` cannot disagree on what
// formatted means — three hand-maintained copies of this list once did.
//
// black comes from the ci profile, so devenv.lock pins its version. It used to
// be `uv tool run black`, which resolves the newest PyPI release at run time
// while the pre-commit hook ran a Nix-pinned one. No --line-length on purpose:
// black's default is the one width that needs no agreement between call sites.
func blackCmd() []string {
	return []string{"black", "."}
}

// pytestDeselected is the marker expression every ordinary pytest run in this
// repo is narrowed by.
//
// It has to name every marker class that is out of scope for an ordinary check,
// not just the newest one: pytest lets a command-line -m override a project's
// own addopts, so passing one marker here would silently re-enable the tests a
// project had deselected for itself. Projects still set the same expression in
// their addopts, which is what covers a bare `pytest` in an editor or a shell.
const pytestDeselected = "not kubernetes and not integration"

// pytestNoTestsExitCode is pytest's exit code for a run that collected nothing
// to execute. Deselecting every test in a project produces it, which is the
// expected outcome for a project whose tests all need a live cluster.
const pytestNoTestsExitCode = 5

// PythonProject is one Python project — a directory with a pyproject.toml —
// carrying only that project's files, so changing one project does not
// invalidate the BuildKit cache for the others.
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
	}
	return projects
}

// PythonProjects returns all discovered Python projects with scoped source
// directories.
func (m *Homelab) PythonProjects(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
) []*PythonProject {
	return pythonProjects(ctx, source, nil)
}

// Test runs pytest for this Python project, in the given toolchain container.
//
// Tests needing something this container hasn't got — a live cluster, a real
// zpool — are deselected by marker, so an ordinary check never reaches for the
// ambient environment. See pytestDeselected.
func (pp *PythonProject) Test(ctx context.Context, container *dagger.Container) (string, error) {
	if err := pp.usable(container); err != nil {
		return "", err
	}

	_, err := container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec([]string{"uv", "run", "--link-mode", "copy", "pytest", "-v", "-m", pytestDeselected}).
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

// pytestRun is one pytest invocation that was allowed to finish whatever its
// verdict, so that its report survives a failing test.
type pytestRun struct {
	// ExitCode is pytest's own exit code.
	ExitCode int
	// Output is the combined pytest log.
	Output string
	// Junit is the JUnit XML report.
	Junit *dagger.File
}

// testKubernetes runs this project's `kubernetes`-marked tests in container,
// which must already carry a KUBECONFIG for the cluster under test. The project
// decides what to assert; this only decides how pytest is invoked, so a new
// release's tests need nothing added here.
//
// pytest's exit code is captured instead of being left to fail the exec,
// because a failed exec's filesystem cannot be read and the JUnit report is
// most wanted exactly when a test failed.
func (pp *PythonProject) testKubernetes(ctx context.Context, container *dagger.Container) (*pytestRun, error) {
	if err := pp.usable(container); err != nil {
		return nil, err
	}

	const (
		outDir       = "/out"
		junitPath    = outDir + "/junit.xml"
		exitCodePath = outDir + "/exit-code"
	)

	run := container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec([]string{"sh", "-c", `
set -u
mkdir -p "$(dirname "$2")"
uv run --link-mode copy pytest -v -m "$1" "--junit-xml=$2" 2>&1
printf %s "$?" > "$3"
`, "--", kubernetesMarker, junitPath, exitCodePath})

	output, err := run.Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("running the %s tests in %s: %w", kubernetesMarker, pp.Path, withExecOutput(err))
	}
	code, err := run.File(exitCodePath).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading pytest's exit code for %s: %w", pp.Path, err)
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		return nil, fmt.Errorf("pytest in %s reported an unreadable exit code %q: %w", pp.Path, code, err)
	}

	return &pytestRun{ExitCode: exitCode, Output: output, Junit: run.File(junitPath)}, nil
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

// FormatPython formats Python files with black across all discovered projects.
// Returns a changeset. Use `dagger call format-python --auto-apply` to apply.
// +generate
func (m *Homelab) FormatPython(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (*dagger.Changeset, error) {
	projects := pythonProjects(ctx, source, paths)
	if len(projects) == 0 {
		return dag.Changeset(), nil
	}
	if container == nil {
		container = m.ciContainer()
	}

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

// TestPython runs pytest for all discovered Python projects.
// +check
func (m *Homelab) TestPython(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	projects := pythonProjects(ctx, source, paths)
	if len(projects) == 0 {
		return "Python tests skipped (no projects found)", nil
	}
	if container == nil {
		container = m.ciContainer()
	}

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
