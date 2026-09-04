package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/homelab/internal/dagger"
)

// VerifyCacheGranularity runs this module's cache-granularity tests, including
// the ones that need a real engine.
//
// `go test ./.dagger/...` on its own only exercises internal/daggerfake's model
// of the engine. The engine-backed tests in engine_cache_test.go skip unless
// they inherit a Dagger session, so they need either
//
//	cd .dagger && dagger run go test ./...
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
func (m *Homelab) VerifyCacheGranularity(
	ctx context.Context,
	// +defaultPath="/"
	// scripts/*.sh is here because gomodhash.go go:embeds it; without it the
	// package will not build inside the container. .golangci.yaml is here
	// because golang_lint_test.go reads the repo's real config rather than a
	// stand-in, so the behaviour tests exercise the config the gate runs with.
	// +ignore=["*", "!.dagger/**/*.go", "!.dagger/go.mod", "!.dagger/go.sum", "!.dagger/scripts/*.sh", "!.golangci.yaml"]
	source *dagger.Directory,
	// Run only tests matching this regexp, as `go test -run`. Defaults to all
	// of them, which covers the fake-backed tests too.
	// +optional
	// +default="."
	run string,
	// The toolchain to run the tests in. It has to carry a Go toolchain; the
	// engine-backed tests additionally need the nesting enabled below.
	// +optional
	container *dagger.Container,
) (string, error) {
	if container == nil {
		container = m.ciContainer()
	}

	out, err := container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/.dagger").
		WithExec(
			[]string{"go", "test", "-count=1", "-v", "-run", run, "./..."},
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
