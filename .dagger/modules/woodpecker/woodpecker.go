// Lints the Woodpecker CI pipelines under .woodpecker/.

package main

import (
	"context"
	"fmt"

	"dagger/woodpecker/internal/dagger"
)

// Woodpecker lints the repo's Woodpecker pipelines.
type Woodpecker struct {
	// +private
	Container *dagger.Container
}

// Lints the Woodpecker CI pipelines.
func New(
	// The container to run in. dagger.toml wires in the nix module's
	// ci-toolchain.
	container *dagger.Container,
) *Woodpecker {
	return &Woodpecker{Container: container}
}

// Lints the Woodpecker CI pipelines.
//
// When paths are provided, only matching files are linted.
// +check
func (m *Woodpecker) Lint(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!.woodpecker/*.yaml"]
	source *dagger.Directory,
	// +optional
	paths []string,
) error {
	container := m.Container
	container = container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src")

	if len(paths) > 0 {
		for _, f := range paths {
			container = container.WithExec([]string{"woodpecker-cli", "lint", "--strict", f})
		}
	} else {
		container = container.WithExec([]string{"woodpecker-cli", "lint", "--strict", ".woodpecker/"})
	}

	_, err := container.Sync(ctx)
	if err != nil {
		return fmt.Errorf("woodpecker lint failed: %w", err)
	}

	return nil
}
