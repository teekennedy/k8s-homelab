package main

import "dagger/homelab/internal/dagger"

// Container image constants with renovate annotations for automated updates.
const (
	// renovate: datasource=docker depName=ghcr.io/cachix/devenv/devenv
	devenvImage = "ghcr.io/cachix/devenv/devenv:v2.0.2"
	// renovate: datasource=docker depName=nixos/nix
	nixImage = "nixos/nix:latest"
	// renovate: datasource=docker depName=golang
	golangImage = "golang:1.26-alpine"
	// renovate: datasource=docker depName=ghcr.io/astral-sh/uv
	uvImage = "ghcr.io/astral-sh/uv:alpine"
	// renovate: datasource=docker depName=cuelang/cue
	cueImage = "cuelang/cue:latest"
	// renovate: datasource=docker depName=cytopia/yamllint
	yamllintImage = "cytopia/yamllint:latest"
	// renovate: datasource=docker depName=ghcr.io/opentofu/opentofu
	opentofuImage = "ghcr.io/opentofu/opentofu:latest"
	// renovate: datasource=docker depName=woodpeckerci/woodpecker-cli
	woodpeckerImage = "woodpeckerci/woodpecker-cli:v3"
	// renovate: datasource=docker depName=alpine/helm
	helmImage = "alpine/helm:latest"
	// renovate: datasource=docker depName=alpine
	alpineImage = "alpine:latest"
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
