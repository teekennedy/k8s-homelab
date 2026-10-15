package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"dagger/homelab/internal/dagger"
)

// A release's Kubernetes integration tests: a Python project at
// k8s/<tier>/<release>/tests, run with pytest against a live cluster.

// kubernetesMarker is the pytest marker carried by tests that need a live
// cluster. Projects deselect it in their own addopts; the integration workflow
// selects it explicitly. See testKubernetes and IntegrationTest.
const kubernetesMarker = "kubernetes"

// kubernetesTestlibPath is the library the releases' integration test projects
// share, relative to the repo root. They import it by relative path, so it has
// to sit where that path leads wherever one of them is run.
const kubernetesTestlibPath = "k8s/testlib"

// pythonSrcRoot is where a project's slice of the repo is laid out in a
// container: the project at its own Path beneath it, so that a relative path
// out of the project resolves as it does in a checkout.
const pythonSrcRoot = "/src"

// testProject is one release's integration test project, carrying only its own
// files and the shared test library.
type testProject struct {
	// Path is the project's directory relative to the repo root.
	Path string
	// Source is the project's scoped source directory.
	Source *dagger.Directory
	// Testlib is the shared integration test library.
	Testlib *dagger.Directory
}

// workdir is the project's directory in a container.
func (pp *testProject) workdir() string {
	return pythonSrcRoot + "/" + pp.Path
}

// usable reports why this project cannot be worked on, if it cannot.
func (pp *testProject) usable(container *dagger.Container) error {
	if pp.Source == nil {
		return fmt.Errorf("test project %s has no source directory", pp.Path)
	}
	if container == nil {
		return fmt.Errorf("test project %s: no toolchain container given", pp.Path)
	}
	return nil
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

// kubernetesEnv installs this project's locked dependencies into toolchain, and
// puts its tests beside them. The install depends on pyproject.toml and uv.lock
// only, so it stays cached when a test is edited. The project is copied rather
// than mounted so the environment lands in the layer. The shared library is
// imported by path, not installed, so it too arrives after the install.
func (pp *testProject) kubernetesEnv(toolchain *dagger.Container) *dagger.Container {
	env := toolchain.
		WithWorkdir(pp.workdir()).
		WithFile("pyproject.toml", pp.Source.File("pyproject.toml")).
		WithFile("uv.lock", pp.Source.File("uv.lock")).
		WithExec([]string{"uv", "sync", "--frozen", "--no-install-project", "--link-mode", "copy"}).
		WithDirectory(".", pp.Source)
	if pp.Testlib != nil {
		env = env.WithDirectory(pythonSrcRoot+"/"+kubernetesTestlibPath, pp.Testlib)
	}
	return env
}

// testKubernetes runs this project's `kubernetes`-marked tests in container,
// which must come from kubernetesEnv and already carry a KUBECONFIG for the
// cluster under test.
//
// pytest's exit code is captured instead of failing the exec, because a failed
// exec's filesystem cannot be read and the JUnit report is wanted most then.
func (pp *testProject) testKubernetes(ctx context.Context, container *dagger.Container) (*pytestRun, error) {
	if err := pp.usable(container); err != nil {
		return nil, err
	}

	const (
		outDir       = "/out"
		junitPath    = outDir + "/junit.xml"
		exitCodePath = outDir + "/exit-code"
	)

	run := container.
		WithExec([]string{"sh", "-c", `
set -u
mkdir -p "$(dirname "$2")"
uv run --no-sync pytest -v -m "$1" "--junit-xml=$2" 2>&1
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
