// Checks on this repo's own Dagger module, beyond what go:test covers.

package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/homelab/internal/dagger"
)

// Ci checks the Dagger module under .dagger.
type Ci struct {
	// +private
	Container *dagger.Container
}

// VerifyCacheGranularity runs the Dagger module's tests, including the ones
// that need a real engine.
//
// The go toolchain's test runs these too, but only against
// internal/daggerfake's model of the engine: the engine-backed tests skip
// unless a run opts in to them under a Dagger session, so they need either
//
//	cd .dagger/modules/homelab && HOMELAB_ENGINE_TESTS=1 dagger run go test ./...
//
// or this function, which opts in, and whose exec gets a session of its own, as
// every container does. Use it to confirm the fake still agrees with the engine after a Dagger
// upgrade, or after teaching the fake a new API call.
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
	// +ignore=["*", "!.dagger/**/*.go", "!.dagger/**/go.mod", "!.dagger/**/go.sum", "!.dagger/**/scripts/*.sh", "!.dagger/modules/homelab/releases.json", "!.golangci.yaml"]
	source *dagger.Directory,
	// Run only tests matching this regexp, as `go test -run`. Defaults to all
	// of them, which covers the fake-backed tests too.
	// +optional
	// +default="."
	run string,
) error {
	container := m.Container

	// Every module, so a module added later is covered without being listed
	// here. Globs rather than find, which the toolchain
	// does not have; a run that tests nothing is a failure, not a pass.
	const script = `
set -u
status=0
tested=0
for mod in modules/*/go.mod; do
	[ -f "$mod" ] || continue
	dir=$(dirname "$mod")
	echo "=== $dir"
	(cd "$dir" && go test -count=1 -run "$1" ./...) || status=1
	tested=$((tested + 1))
done
if [ "$tested" -eq 0 ]; then
	echo "no Go modules found under .dagger" >&2
	exit 1
fi
exit $status
`

	out, err := container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/.dagger").
		// The opt-in and the exec's own Dagger session are what make the
		// engine-backed tests run for real. Their fixtures are planted in this
		// container's temp dir and loaded back off its filesystem by the nested
		// client.
		WithEnvVariable("HOMELAB_ENGINE_TESTS", "1").
		WithExec([]string{"sh", "-c", script, "--", run}).
		Stdout(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return fmt.Errorf("cache granularity tests failed:\n%s%s", execErr.Stdout, execErr.Stderr)
		}
		return fmt.Errorf("cache granularity tests failed: %w\n%s", err, out)
	}
	return nil
}
