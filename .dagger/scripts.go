package main

import (
	"context"
	"fmt"

	"dagger/homelab/internal/dagger"
)

// TestScripts runs the script regression tests with their repository inputs.
// +check
func (m *Homelab) TestScripts(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!scripts/tests/test_release.py", "!scripts/tests/pyproject.toml", "!scripts/tests/uv.lock", "!scripts/release.sh", "!scripts/release-metadata.sh", "!.dagger/releases.json", "!cliff.toml", "!k8s/platform/forgejo/files/forgejo-resources/cliff.toml", "!k8s/apps/openhands/files/sandbox/cliff.toml"]
	source *dagger.Directory,
	// +optional
	container *dagger.Container,
) (string, error) {
	if container == nil {
		container = m.ciContainer()
	}
	_, err := container.
		WithMountedDirectory("/repo", source).
		WithWorkdir("/repo/scripts/tests").
		WithExec([]string{"uv", "run", "--frozen", "--link-mode", "copy", "pytest", "-v"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("script tests failed: %w", withExecOutput(err))
	}
	return "Script tests passed", nil
}
