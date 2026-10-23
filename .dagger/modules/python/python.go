// Tests and formats every Python project in the repo, each in its own scoped
// container so that a change to one project leaves the others cached.

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"dagger/python/internal/dagger"
)

const (
	// scriptTestsPath is the release scripts' test project. release/test
	// runs it, with the scripts and manifests it reads mounted beside it.
	scriptTestsPath = "scripts/tests"

	// pytestNoTestsExitCode is pytest's exit code for a run that collected
	// nothing to execute. A project whose tests all deselect themselves — one
	// holding only cluster tests, say — produces it, and that is a pass rather
	// than a failure.
	pytestNoTestsExitCode = 5
)

// Python runs Python checks over every project in the repo.
type Python struct {
	// +private
	Container *dagger.Container
}

// Tests, formats and locks every Python project in the repo.
func New(
	// The container to run in. dagger.toml wires in the nix module's
	// ci-toolchain.
	container *dagger.Container,
) *Python {
	return &Python{Container: container}
}

// blackCmd is the single definition of how black is invoked.
//
// black comes from the ci profile, so devenv.lock pins its version. It used to
// be `uv tool run black`, which resolves the newest PyPI release at run time
// while the pre-commit hook ran a Nix-pinned one. No --line-length on purpose:
// black's default is the one width that needs no agreement between call sites.
func blackCmd() []string {
	return []string{"black", "."}
}

// Projects returns every Python project in the repo — every directory with a
// pyproject.toml — keyed by path.
func (m *Python) Projects(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.py", "!**/pyproject.toml", "!**/uv.lock", "!**/tests/fixtures/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
) (*PythonProjects, error) {
	pyprojectFiles, err := source.Glob(ctx, "**/pyproject.toml")
	if err != nil {
		return nil, fmt.Errorf("discovering Python projects: %w", err)
	}
	var paths []string
	for _, f := range pyprojectFiles {
		if dir := filepath.Dir(f); dir != "." {
			paths = append(paths, dir)
		}
	}
	sort.Strings(paths)
	return &PythonProjects{Paths: paths, Source: source, Container: m.Container}, nil
}

// PythonProjects are the repo's Python projects.
// +collection
type PythonProjects struct {
	// Paths are the projects' directories relative to the repo root.
	// +keys
	Paths []string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
}

// Project returns the Python project at path, carrying only its own files, so
// that it caches independently of the others.
// +get
func (ps *PythonProjects) Project(path string) *PythonProject {
	project := &PythonProject{Path: path, Source: ps.Source.Directory(path), Container: ps.Container}
	if usesTestlib(path) {
		project.Testlib = ps.Source.Directory(kubernetesTestlibPath)
	}
	return project
}

// PythonProject is one Python project.
type PythonProject struct {
	// Path is the project's directory relative to the repo root
	// (e.g. "k8s/foundation/kured/files/kured-webhook").
	Path string
	// +private
	Source *dagger.Directory
	// Testlib is the shared integration test library, set only for the
	// projects that import it.
	// +private
	Testlib *dagger.Directory
	// +private
	Container *dagger.Container
}

const (
	// kubernetesTestlibPath is the library a release's integration tests share.
	kubernetesTestlibPath = "k8s/testlib"
	// kubernetesTestsDir is the directory in a release's chart that holds its
	// integration tests.
	kubernetesTestsDir = "tests"
	// pythonSrcRoot is where a project's slice of the repo is laid out in a
	// container: the project at its own Path beneath it, so that a relative
	// path out of the project resolves as it does in a checkout.
	pythonSrcRoot = "/src"
)

// usesTestlib reports whether the project at projPath is a release's
// integration test project: k8s/<tier>/<release>/tests.
func usesTestlib(projPath string) bool {
	parts := strings.Split(projPath, "/")
	return len(parts) == 4 && parts[0] == "k8s" && parts[3] == kubernetesTestsDir
}

// Runs the project's pytest suite.
//
// No -m here on purpose. Which markers an ordinary run excludes is a per-project
// decision, declared in each pyproject.toml's addopts — and it has to be,
// because a command-line -m replaces addopts rather than narrowing it. Passing
// one from here would re-enable the tests a project deselected for itself, and
// deselect the ones a project runs deliberately: jellyfin-exporter registers
// `integration` precisely so those tests run by default against its stub server.
// +check
func (pp *PythonProject) Test(ctx context.Context) error {
	if pp.Path == scriptTestsPath {
		return nil
	}

	workdir := pythonSrcRoot + "/" + pp.Path
	// The library is mounted even though these runs deselect the tests that
	// use it: collecting them still imports their conftest.
	container := pp.Container.WithMountedDirectory(workdir, pp.Source)
	if pp.Testlib != nil {
		container = container.WithMountedDirectory(pythonSrcRoot+"/"+kubernetesTestlibPath, pp.Testlib)
	}
	// A project whose tests all deselect themselves exits with
	// pytestNoTestsExitCode, which is a pass. It is turned into one inside the
	// exec rather than after it: a failed exec is never cached, so it would
	// re-run on every `dagger check`.
	_, err := container.
		WithWorkdir(workdir).
		WithExec([]string{
			"sh", "-c", `uv run --frozen --link-mode copy pytest; status=$?; [ "$status" -eq "$1" ] && exit 0; exit "$status"`,
			"--", strconv.Itoa(pytestNoTestsExitCode),
		}).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("pytest failed in %s: %w", pp.Path, withExecOutput(err))
	}
	return nil
}

// Formats the project with black.
// +generate
func (pp *PythonProject) Format(ctx context.Context) (*dagger.Changeset, error) {
	formatted, err := pp.Container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec(blackCmd()).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("black failed in %s: %w", pp.Path, withExecOutput(err))
	}

	return rerooted(pp.Path, pp.Source, formatted.Directory("/src")), nil
}

// Refreshes the project's uv.lock to match pyproject.toml.
//
// It runs `uv lock`. A project without a uv.lock is left alone. uv comes from
// the toolchain, so the lock is written by the same uv version as everything
// else.
// +generate
func (pp *PythonProject) Lock(ctx context.Context) (*dagger.Changeset, error) {
	locks, err := pp.Source.Glob(ctx, "uv.lock")
	if err != nil {
		return nil, fmt.Errorf("looking for %s/uv.lock: %w", pp.Path, err)
	}
	if len(locks) == 0 {
		return dag.Changeset(), nil
	}

	locked, err := pp.Container.
		WithMountedDirectory("/src", pp.Source).
		WithWorkdir("/src").
		WithExec([]string{"uv", "lock"}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("uv lock failed in %s: %w", pp.Path, withExecOutput(err))
	}

	return rerooted(pp.Path, pp.Source, locked.Directory("/src")), nil
}
