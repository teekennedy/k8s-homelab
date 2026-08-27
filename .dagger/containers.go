package main

import (
	"dagger/homelab/internal/dagger"
	"fmt"
)

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=ghcr.io/cachix/devenv/devenv
	devenvImage = "ghcr.io/cachix/devenv/devenv:v2.2.2"
	// renovate: datasource=docker depName=nixos/nix
	nixImage = "nixos/nix:2.35.2"
	// renovate: datasource=docker depName=golang
	golangImage = "golang:1.27-alpine"
	// renovate: datasource=docker depName=golangci/golangci-lint
	golangciLintImage = "golangci/golangci-lint:v2.13.0-alpine"
	// renovate: datasource=docker depName=ghcr.io/astral-sh/uv
	uvImage = "ghcr.io/astral-sh/uv:0.12.6-alpine"
	// renovate: datasource=docker depName=cuelang/cue
	cueImage = "cuelang/cue:0.17.1"
	// renovate: datasource=docker depName=cytopia/yamllint
	yamllintImage = "cytopia/yamllint:1"
	// renovate: datasource=docker depName=ghcr.io/opentofu/opentofu
	opentofuImage = "ghcr.io/opentofu/opentofu:1.12.6"
	// renovate: datasource=docker depName=woodpeckerci/woodpecker-cli
	woodpeckerImage = "woodpeckerci/woodpecker-cli:v3"
	// renovate: datasource=docker depName=alpine/helm
	helmImage = "alpine/helm:4.2.4"
	// renovate: datasource=docker depName=alpine
	alpineImage = "alpine:3.24.1"
	// renovate: datasource=docker depName=us-docker.pkg.dev/fairwinds-ops/oss/polaris
	polarisImage = "us-docker.pkg.dev/fairwinds-ops/oss/polaris:v10.2.2"
	// renovate: datasource=docker depName=ghcr.io/yannh/kubeconform
	kubeconformImage = "ghcr.io/yannh/kubeconform:v0.8.0"
)

func nixContainer() *dagger.Container {
	return dag.Container().From(nixImage)
}

func golangContainer() *dagger.Container {
	gomodcache := "/go/pkg/mod"
	return dag.Container().
		From(golangImage).
		WithEnvVariable("GOMODCACHE", gomodcache).
		WithMountedCache(gomodcache, dag.CacheVolume("GOMODCACHE-"+golangImage))
}

// golangciLintContainer returns a golangContainer with the golangci-lint binary
// installed (copied in from the official image, so the Go toolchain version
// stays pinned to golangImage), plus a persistent cache for golangci-lint's
// own result cache (~/.cache/golangci-lint).
func golangciLintContainer() *dagger.Container {
	lintBin := dag.Container().From(golangciLintImage).File("/usr/bin/golangci-lint")
	lintCache := "/root/.cache/golangci-lint"
	cacheKey := fmt.Sprintf("golangci-lint-cache-%s", golangciLintImage)

	return golangContainer().
		WithFile("/usr/local/bin/golangci-lint", lintBin).
		WithMountedCache(lintCache, dag.CacheVolume(cacheKey))
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
		// Suppress zsh-specific setup (compdef errors) and version nag in container context
		WithEnvVariable("DEVENV_ZSH_DISABLE", "1")
}
