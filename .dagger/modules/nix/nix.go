// Formats, validates and tests the Nix flake and host configuration.

package main

import (
	"context"
	_ "embed"
	"fmt"

	"dagger/nix/internal/dagger"
)

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=nixos/nix
	nixImageRef = "nixos/nix:2.35.3@sha256:ff480dbb23511aa1c10710aa068ceea5b93fed9bfce5f5379f3a6b07282cbc7b"
)

//go:embed scripts/nix-build-metrics-probe.sh
var nixBuildMetricsProbe string

// Image returns the plain Nix image, for evaluating and building flakes.
func (m *Nix) Image() *dagger.Container {
	return nixImage()
}

// nixImage is the plain Nix image, for evaluating and building flakes.
func nixImage() *dagger.Container {
	return dag.Container().From(nixImageRef)
}

// nixImageStore is the Nix image with a persistent store (see nixStore) and
// Nix's fetcher and evaluation caches, so a build only fetches or builds what
// changed since the last one. The volumes are keyed by the image, whose own
// Nix lives in the store they are seeded from.
func nixImageStore(signedOnly bool) *dagger.Container {
	key := "homelab-nix-store-"
	if signedOnly {
		key = "homelab-nix-store-signed-"
	}
	return nixImage().
		WithMountedCache("/nix", dag.CacheVolume(key+nixImageRef), dagger.ContainerWithMountedCacheOpts{
			Source: nixImage().Directory("/nix"),
		}).
		WithMountedCache("/root/.cache/nix", dag.CacheVolume("homelab-nix-cache-home"))
}

// nixCommand is a nix invocation with the features every flake command needs.
func nixCommand(args ...string) []string {
	return append([]string{"nix", "--extra-experimental-features", "nix-command flakes"}, args...)
}

// Formats the Nix files with alejandra and deadnix.
//
// deadnix removes dead code first, which can leave formatting for alejandra to
// clean up.
// +generate
func (m *Nix) Format(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.nix", ".devenv*", ".devenv/**", "devenv.local.*"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (*dagger.Changeset, error) {
	toolchain, err := m.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return nixFormat(toolchain, source, paths).Changes(source), nil
}

// nixFormat runs alejandra and deadnix on source, returning the formatted directory.
//
// Both come from the ci profile rather than `nix-env -iA nixpkgs.*` in the nix
// image, which installed whatever version that image's channel happened to
// carry — a different alejandra than the one devenv.lock pins would reformat
// the tree back and forth between a local run and a CI run.
func nixFormat(toolchain *dagger.Container, source *dagger.Directory, paths []string) *dagger.Directory {
	container := toolchain.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src")

	targets := []string{"."}
	if len(paths) > 0 {
		targets = paths
	}

	return container.
		WithExec(append([]string{"deadnix", "--edit"}, targets...)).
		WithExec(append([]string{"alejandra"}, targets...)).
		Directory("/src")
}

// Evaluates the flake with `nix flake check`.
// +check
func (m *Nix) Validate(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!cmd/lab/flake.nix", "!cmd/lab/flake.lock", "!cmd/lab/default.nix", "!cmd/lab/gomod.json"]
	source *dagger.Directory,
) error {
	checked, err := m.Remote.build(ctx, nixImageStore, func(env nixEnv) *dagger.Container {
		return env.Container.
			WithMountedDirectory("/src", source).
			WithWorkdir("/src").
			WithExec(env.Exec(nixCommand("flake", "check", "--no-build")))
	})
	if err == nil {
		_, err = checked.Sync(ctx)
	}
	if err != nil {
		return fmt.Errorf("nix flake check failed: %w", withExecOutput(err))
	}

	return nil
}

// Tests the build-metrics parser on a real Nix build.
//
// The selfupdate module's parser reads Nix's internal-json event stream, so a
// Nix upgrade that changes it (NixOS/nix#13935) is caught here instead of only
// on a host.
// +check
func (m *Nix) Test(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!flake.lock", "!nix/modules/selfupdate/build-metrics/**", "**/.venv/**", "**/__pycache__/**", "**/.pytest_cache/**"]
	source *dagger.Directory,
) error {
	const project = "nix/modules/selfupdate/build-metrics"

	probe := m.Image().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithNewFile("/probe.sh", nixBuildMetricsProbe, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithExec([]string{"/probe.sh"})

	toolchain, err := m.Toolchains.CiToolchain(ctx)
	if err != nil {
		return err
	}
	_, err = toolchain.
		WithMountedDirectory("/src", source.Directory(project)).
		WithMountedFile("/tmp/nix-log.json", probe.File("/out/nix-log.json")).
		WithMountedFile("/tmp/timings.txt", probe.File("/out/timings.txt")).
		WithWorkdir("/src").
		WithEnvVariable("NIX_BUILD_METRICS_LOG", "/tmp/nix-log.json").
		WithEnvVariable("NIX_BUILD_METRICS_TIMINGS", "/tmp/timings.txt").
		WithExec([]string{"uv", "run", "--no-cache", "--link-mode", "copy", "pytest"}).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("nix build metrics parser failed: %w", withExecOutput(err))
	}

	return nil
}
