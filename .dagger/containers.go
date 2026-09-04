package main

import (
	"dagger/homelab/internal/dagger"
)

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=ghcr.io/cachix/devenv/devenv
	devenvImage = "ghcr.io/cachix/devenv/devenv:v2.2.2"
	// renovate: datasource=docker depName=nixos/nix
	nixImage = "nixos/nix:2.35.2"
)

func nixContainer() *dagger.Container {
	return dag.Container().From(nixImage)
}

// devenvContainer returns a devenv container with a persistent nix store cache
// mounted, so nix/devenv operations only build or fetch what changed. The
// cache volume name includes the image tag so it auto-invalidates when the
// devenv image is updated (e.g. by Renovate).
func devenvContainer() *dagger.Container {
	nixCacheKey := "devenv-nix-" + devenvImage
	baseNix := dag.Container().From(devenvImage).WithUser("root").Directory("/nix")

	return dag.Container().
		From(devenvImage).
		WithUser("root").
		WithMountedCache("/nix", dag.CacheVolume(nixCacheKey), dagger.ContainerWithMountedCacheOpts{
			Source: baseNix,
		}).
		// Suppress zsh-specific setup (compdef errors) in container context
		WithEnvVariable("DEVENV_ZSH_DISABLE", "1")
}

// ciProfiles is the devenv profile set the check functions run in.
var ciProfiles = []string{"ci"}

// ciContainer returns the devenv "ci" profile as a container, ready to run a
// check in. It is lazy: nothing here talks to the engine, so the result can be
// rebuilt from a *dagger.Directory anywhere in the module rather than being
// built once and passed around as state.
func ciContainer(devenvSource *dagger.Directory) *dagger.Container {
	return withToolchainCaches(devenvShell(devenvSource, nil, ciProfiles))
}

// withToolchainCaches attaches the caches every language toolchain in the ci
// profile wants.
//
// They are attached once here rather than at each call site so that a Go check
// and a Python check running in parallel share one cache volume each, and so
// adding a tool to the ci profile doesn't mean remembering to wire up its cache
// separately.
//
// Every path is named by the tool's own environment variable rather than left
// to default under $HOME. The devenv image sets HOME=/env and User=user, and
// hanging caches off that would couple the mount layout to devenv's container
// internals; the execs run as root so the mounts are writable.
func withToolchainCaches(c *dagger.Container) *dagger.Container {
	const (
		goModCache     = "/cache/go/mod"
		goBuildCache   = "/cache/go/build"
		uvCache        = "/cache/uv"
		helmCacheHome  = "/cache/helm"
		helmConfigHome = "/config/helm"
		helmDataHome   = "/data/helm"
	)

	return c.
		WithUser("root").
		// Create statically linked go binaries
		WithEnvVariable("CGO_ENABLED", "0").
		// Do not stamp go binaries with version control information
		WithEnvVariable("GOFLAGS", "-buildvcs=false").
		WithEnvVariable("GOMODCACHE", goModCache).
		WithMountedCache(goModCache, dag.CacheVolume("homelab-go-mod")).
		WithEnvVariable("GOCACHE", goBuildCache).
		WithMountedCache(goBuildCache, dag.CacheVolume("homelab-go-build")).
		WithEnvVariable("UV_CACHE_DIR", uvCache).
		WithMountedCache(uvCache, dag.CacheVolume("homelab-uv")).
		WithEnvVariable("HELM_CACHE_HOME", helmCacheHome).
		WithMountedCache(helmCacheHome, dag.CacheVolume("homelab-helm-cache")).
		WithEnvVariable("HELM_CONFIG_HOME", helmConfigHome).
		WithMountedCache(helmConfigHome, dag.CacheVolume("homelab-helm-config")).
		WithEnvVariable("HELM_DATA_HOME", helmDataHome).
		WithMountedCache(helmDataHome, dag.CacheVolume("homelab-helm-data"))
}
