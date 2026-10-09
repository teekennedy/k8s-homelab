package main

import (
	"dagger/homelab/internal/dagger"
)

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=ghcr.io/cachix/devenv/devenv
	devenvImage = "ghcr.io/cachix/devenv/devenv:v2.4.0"
	// renovate: datasource=docker depName=nixos/nix
	nixImage = "nixos/nix:2.35.2"
	// k3sImage is what the ephemeral clusters run. Keep its minor version in
	// step with the production cluster's.
	// renovate: datasource=docker depName=rancher/k3s
	k3sImage = "rancher/k3s:v1.36.5-k3s1"
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
	return toolchainContainer(devenvSource, ciProfiles)
}

// integrationProfiles adds the cluster tooling the Kubernetes integration
// workflow drives — kubectl and curl — on top of the ci toolchain.
//
// A separate profile rather than more packages in ci: every check runs in the
// ci container, and none of them has any use for a Kubernetes client. This way
// `dagger check` keeps running in a container that cannot reach a cluster even
// if something tried.
var integrationProfiles = []string{"ci", "integration"}

// integrationContainer returns the toolchain the Kubernetes integration
// workflow runs in.
func integrationContainer(devenvSource *dagger.Directory) *dagger.Container {
	return toolchainContainer(devenvSource, integrationProfiles)
}

// toolchainContainer builds the devenv shell for profiles, with the toolchain
// caches attached.
func toolchainContainer(devenvSource *dagger.Directory, profiles []string) *dagger.Container {
	return withToolchainCaches(devenvShell(devenvSource, nil, profiles))
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
		helmfileCache  = "/cache/helmfile"
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
		WithMountedCache(helmDataHome, dag.CacheVolume("homelab-helm-data")).
		WithEnvVariable("HELMFILE_CACHE_HOME", helmfileCache).
		WithMountedCache(helmfileCache, dag.CacheVolume("homelab-helmfile"))
}
