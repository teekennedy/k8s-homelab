// Checks on this repo's own Dagger module, beyond what go:test covers.

package main

import (
	"context"
	"fmt"

	"dagger/homelab/internal/dagger"
)

// Ci checks the Dagger module under .dagger.
type Ci struct {
	// +private
	Container *dagger.Container
}

// IntegrationTest runs the module's tests with the ones that need a real
// engine opted in: daggerfake's conformance tests, which hold the fake the
// cache-granularity tests run against to the engine it models, and
// TestLintBehaviour, which runs golangci-lint for real.
//
// go/test runs the same tests with those skipped. Run this after a Dagger
// upgrade or after teaching the fake a new API call. Its result caches on the
// module's source, and an engine upgrade starts from an empty cache.
// +check
func (m *Ci) IntegrationTest(
	ctx context.Context,
	// +defaultPath="/"
	// .golangci.yaml is here because TestLintBehaviour reads the repo's real
	// config rather than a stand-in.
	// +ignore=["*", "!.dagger/modules/homelab/**", "!.golangci.yaml"]
	source *dagger.Directory,
) error {
	_, err := m.Container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/.dagger/modules/homelab").
		// The exec has a Dagger session of its own, as every container does;
		// the opt-in (enginetest.Env, which only test code may import) is what
		// makes the tests use it.
		WithEnvVariable("HOMELAB_ENGINE_TESTS", "1").
		WithExec([]string{"go", "test", "-count=1", "./..."}).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("integration tests failed: %w", withExecOutput(err))
	}
	return nil
}
