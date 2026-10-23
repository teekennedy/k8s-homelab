package main

import (
	"context"
	"fmt"

	"dagger/release/internal/dagger"
)

// Runs the release scripts' regression tests.
//
// The tests read their repository inputs: the scripts, this module's
// releases.json and the git-cliff configs.
// +check
func (m *Release) Test(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!scripts/tests/test_release.py", "!scripts/tests/pyproject.toml", "!scripts/tests/uv.lock", "!scripts/release.sh", "!scripts/release-metadata.sh", "!scripts/record-devenv-ci.sh", "!.dagger/modules/release/releases.json", "!cliff.toml", "!k8s/platform/forgejo/files/forgejo-resources/cliff.toml", "!k8s/apps/openhands/files/sandbox/cliff.toml"]
	source *dagger.Directory,
) error {
	container := m.Container
	_, err := container.
		WithMountedDirectory("/repo", source).
		WithWorkdir("/repo/scripts/tests").
		WithExec([]string{"uv", "run", "--frozen", "--link-mode", "copy", "pytest"}).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("script tests failed: %w", withExecOutput(err))
	}
	return nil
}
