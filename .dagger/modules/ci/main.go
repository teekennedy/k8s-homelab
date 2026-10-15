// Checks on this repo's own Dagger modules, beyond what go:test covers.
package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/ci/internal/dagger"
)

// Ci checks the Dagger modules under .dagger.
type Ci struct {
	// +private
	Container *dagger.Container
}

// Checks on this repo's own Dagger modules.
func New(
	// Container to run in. It needs go; defaults to the devenv ci toolchain.
	// +optional
	container *dagger.Container,
) *Ci {
	return &Ci{Container: container}
}

// VerifyCacheGranularity runs the Dagger modules' tests, including the ones
// that need a real engine.
//
// The go toolchain's test runs these too, but only against
// dagger/lib/daggerfake's model of the engine: the engine-backed tests skip unless they inherit a Dagger
// session, so they need either
//
//	cd .dagger/modules/<module> && dagger run go test ./...
//
// or this function, which gets the same thing by running the tests in an exec
// with nesting enabled. Use it to confirm the fake still agrees with the engine
// after a Dagger upgrade, or after teaching the fake a new API call.
//
// Its result caches on its inputs, like any other function here, and that is
// the right behaviour: the two things that can change the verdict both bust it.
// Editing .dagger changes source, and upgrading Dagger means a new engine with
// a new cache. To force a re-run anyway, vary run (`--run '.*'` is a different
// argument to the default `.` and so a different call).
// +check
func (m *Ci) VerifyCacheGranularity(
	ctx context.Context,
	// +defaultPath="/"
	// .golangci.yaml is here because go's lint_test.go reads the repo's real
	// config rather than a stand-in, so the behaviour tests exercise the
	// config the gate runs with.
	// +ignore=["*", "!.dagger/**/*.go", "!.dagger/**/go.mod", "!.dagger/**/go.sum", "!.dagger/**/scripts/*.sh", "!.dagger/modules/release/releases.json", "!.golangci.yaml"]
	source *dagger.Directory,
	// Run only tests matching this regexp, as `go test -run`. Defaults to all
	// of them, which covers the fake-backed tests too.
	// +optional
	// +default="."
	run string,
) (string, error) {
	container := m.Container
	if container == nil {
		container = dag.Devenv().Toolchain()
	}

	// Every Go module under .dagger, so a module added later is covered
	// without being listed here.
	const script = `
set -u
status=0
for mod in $(find . -name go.mod -not -path '*/vendor/*' | sort); do
	dir=$(dirname "$mod")
	echo "=== $dir"
	(cd "$dir" && go test -count=1 -run "$1" ./...) || status=1
done
exit $status
`

	out, err := container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/.dagger").
		WithExec(
			[]string{"sh", "-c", script, "--", run},
			dagger.ContainerWithExecOpts{
				// Gives the exec its own Dagger session, so cacheprobe steps
				// aside and the engine-backed tests run for real. Their fixtures
				// are planted in this container's temp dir and loaded back off
				// its filesystem by the nested client.
				ExperimentalPrivilegedNesting: true,
			},
		).
		Stdout(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return "", fmt.Errorf("cache granularity tests failed:\n%s%s", execErr.Stdout, execErr.Stderr)
		}
		return "", fmt.Errorf("cache granularity tests failed: %w\n%s", err, out)
	}
	return out, nil
}

// DebugDir returns the given dir. Useful for inspecting directory contents for debugging.
func (m *Ci) DebugDir(dir *dagger.Directory) *dagger.Directory {
	return dir
}
