package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/release/internal/dagger"
)

// TestScripts runs the release script regression tests with their repository
// inputs: the scripts, this module's releases.json and the cliff configs.
// +check
func (m *Release) TestScripts(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!scripts/tests/test_release.py", "!scripts/tests/pyproject.toml", "!scripts/tests/uv.lock", "!scripts/release.sh", "!scripts/release-metadata.sh", "!.dagger/modules/release/releases.json", "!cliff.toml", "!k8s/platform/forgejo/files/forgejo-resources/cliff.toml", "!k8s/apps/openhands/files/sandbox/cliff.toml"]
	source *dagger.Directory,
) (string, error) {
	container := m.Container
	if container == nil {
		container = dag.Devenv().Toolchain()
	}
	_, err := container.
		WithMountedDirectory("/repo", source).
		WithWorkdir("/repo/scripts/tests").
		WithExec([]string{"uv", "run", "--frozen", "--link-mode", "copy", "pytest"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return "", fmt.Errorf("script tests failed: %w\n%s%s", err, execErr.Stdout, execErr.Stderr)
		}
		return "", fmt.Errorf("script tests failed: %w", err)
	}
	return "Script tests passed", nil
}
