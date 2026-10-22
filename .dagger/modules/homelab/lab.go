// Builds the lab CLI (cmd/lab), with Nix as production does or with Go for
// speed, and keeps its Nix vendorHash in step with go.sum. Its tests and lint
// run with every other Go module, under the go toolchain.

package main

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"dagger/homelab/internal/dagger"
)

// Lab builds the lab CLI.
type Lab struct {
	// +private
	Container *dagger.Container
	// +private
	Builders *NixBuilders
}

// Build builds the lab CLI binary using Nix.
// This ensures the build uses the exact same process as production.
// +check
func (m *Lab) Build(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*"]
	source *dagger.Directory,
) error {
	built, err := m.nixBuild(ctx, source, "--no-link", "--print-build-logs")
	if err == nil {
		_, err = built.Sync(ctx)
	}
	if err != nil {
		return fmt.Errorf("nix build failed: %w", err)
	}

	return nil
}

// Binary returns the built lab CLI binary, built with Go for speed.
func (m *Lab) Binary(
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*.go", "!cmd/lab/go.mod", "!cmd/lab/go.sum"]
	source *dagger.Directory,
	// Platform to build for. Defaults to the engine's.
	// +optional
	platform dagger.Platform,
) *dagger.File {
	// The devenv container only exists for the engine's own platform, so the
	// target platform is honoured by cross-compiling with GOOS/GOARCH rather
	// than by pulling a toolchain image built for it. CGO is already off, so
	// this produces the same static binary either way.
	goos, goarch := splitPlatform(platform)

	return m.Container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/cmd/lab").
		WithEnvVariable("CGO_ENABLED", "0").
		WithEnvVariable("GOOS", goos).
		WithEnvVariable("GOARCH", goarch).
		WithExec([]string{"go", "build", "-ldflags", "-s -w", "-o", "/out/lab", "."}).
		File("/out/lab")
}

// splitPlatform turns a Dagger platform ("linux/arm64/v8") into the GOOS and
// GOARCH that name it. The optional variant suffix is dropped: Go selects arm
// variants with GOARM, and none of the platforms this module builds for need
// it. An empty platform is the engine's own.
func splitPlatform(platform dagger.Platform) (goos, goarch string) {
	parts := strings.Split(string(platform), "/")
	if len(parts) < 2 {
		return runtime.GOOS, runtime.GOARCH
	}
	return parts[0], parts[1]
}

// BinaryNix returns the built lab CLI binary, built with Nix.
func (m *Lab) BinaryNix(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*"]
	source *dagger.Directory,
) (*dagger.File, error) {
	built, err := m.nixBuild(ctx, source, "--out-link", "/tmp/result")
	if err != nil {
		return nil, err
	}
	// The result links into the store, a cache volume that is not part of the
	// container's filesystem, so the binary is copied out of it.
	return built.
		WithExec([]string{"cp", "-L", "/tmp/result/bin/lab", "/tmp/lab"}).
		File("/tmp/lab"), nil
}

// nixBuild builds the lab CLI's flake with args.
func (m *Lab) nixBuild(ctx context.Context, source *dagger.Directory, args ...string) (*dagger.Container, error) {
	return m.Builders.build(ctx, nixImageStore, func(env nixEnv) *dagger.Container {
		return env.Container.
			WithMountedDirectory("/src", source).
			WithWorkdir("/src").
			WithExec(env.Exec(nixCommand(append([]string{"build", "./cmd/lab"}, args...)...)))
	})
}
