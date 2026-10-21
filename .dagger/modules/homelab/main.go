// CI for this repo: tests, linters, formatters and builds for every language
// and platform in it, and the containers they run in.
//
// Each language or platform is an object of its own (go, python, helmfile,
// ...), so `dagger check python` or `dagger api call go modules` select one.
// They all run in the devenv toolchain, built here from devenv.lock, so a check
// runs the same tools as a local `devenv shell`.
package main

import (
	"context"

	"dagger/homelab/internal/dagger"
)

// Homelab is the repo's CI.
type Homelab struct {
	// +private
	Toolchains *Devenv
}

// CI for this repo: tests, linters, formatters and builds for every language and platform in it.
func New(
	// The files devenv reads to build any profile: devenv.nix, devenv.yaml,
	// devenv.lock, and all of cmd/lab, which devenv.yaml imports and whose CLI
	// the interactive profile builds from source.
	// +defaultPath="/"
	// +ignore=["*", "!devenv.nix", "!devenv.yaml", "!devenv.lock", "!cmd/lab/**", "cmd/lab/result", "cmd/lab/.direnv"]
	devenvSource *dagger.Directory,
	// devenvSource without the lab CLI's own source, which the toolchain the
	// checks run in never builds, so that editing it leaves the toolchain
	// cached. Loaded separately rather than filtered out of devenvSource, so
	// that it is content-addressed from the client filesystem: a directory
	// derived from devenvSource would carry its identity, Go files and all.
	// Keep it in step with toolchainFiles.
	// +defaultPath="/"
	// +ignore=["*", "!devenv.nix", "!devenv.yaml", "!devenv.lock", "!cmd/lab/devenv.nix"]
	toolchainSource *dagger.Directory,
) *Homelab {
	return &Homelab{Toolchains: &Devenv{Source: devenvSource, ToolchainSource: toolchainSource}}
}

// Devenv builds the devenv shell as a container: the toolchain the checks run
// in, and the devenv images releases publish.
func (h *Homelab) Devenv() *Devenv {
	return h.Toolchains
}

// Ci checks this repo's Dagger module, beyond what go's checks cover.
func (h *Homelab) Ci(ctx context.Context) *Ci {
	return &Ci{Container: h.Toolchains.CiToolchain(ctx)}
}

// Cue formats, upgrades, trims and exports the CUE configuration under config/.
func (h *Homelab) Cue(ctx context.Context) *Cue {
	return &Cue{Container: h.Toolchains.CiToolchain(ctx)}
}

// Go tests and lints every Go module in the repo.
func (h *Homelab) Go(ctx context.Context) *Go {
	return &Go{Container: h.Toolchains.CiToolchain(ctx)}
}

// Helmfile renders, lints and audits every chart with the repo's helmfile.
func (h *Homelab) Helmfile(
	ctx context.Context,
	// Helmfile environments to check every chart in.
	// +optional
	// +default=["production"]
	environments []string,
) *Helmfile {
	return &Helmfile{Container: h.Toolchains.CiToolchain(ctx), EnvironmentNames: environments}
}

// Kubernetes runs releases on throwaway or long-lived k3s clusters and tests
// them there. It runs in the ci and integration profiles, which no release
// publishes, so it is always built from source.
func (h *Homelab) Kubernetes() *Kubernetes {
	return &Kubernetes{Container: h.Toolchains.IntegrationToolchain()}
}

// Lab builds the lab CLI and keeps its Nix vendorHash current.
func (h *Homelab) Lab(ctx context.Context) *Lab {
	return &Lab{Container: h.Toolchains.CiToolchain(ctx), Nix: nixImage()}
}

// Nix formats and validates the Nix flake and host configuration.
func (h *Homelab) Nix(ctx context.Context) *Nix {
	return &Nix{Container: h.Toolchains.CiToolchain(ctx)}
}

// Python tests and formats every Python project in the repo.
func (h *Homelab) Python(ctx context.Context) *Python {
	return &Python{Container: h.Toolchains.CiToolchain(ctx)}
}

// Release builds, labels and publishes the repo's container images.
func (h *Homelab) Release(ctx context.Context) *Release {
	return &Release{Container: h.Toolchains.CiToolchain(ctx), Devenv: h.Toolchains}
}

// Terraform formats, lints, locks and validates the OpenTofu modules under terraform/.
func (h *Homelab) Terraform(
	ctx context.Context,
	// Platforms the lock generator computes provider hashes for.
	// +optional
	// +default=["linux_arm64", "linux_amd64", "darwin_arm64"]
	platforms []string,
) *Terraform {
	return &Terraform{Container: h.Toolchains.CiToolchain(ctx), Platforms: platforms}
}

// Woodpecker lints the Woodpecker CI pipelines.
func (h *Homelab) Woodpecker(ctx context.Context) *Woodpecker {
	return &Woodpecker{Container: h.Toolchains.CiToolchain(ctx)}
}

// Yaml lints every YAML file in the repo.
func (h *Homelab) Yaml(ctx context.Context) *Yaml {
	return &Yaml{Container: h.Toolchains.CiToolchain(ctx)}
}
