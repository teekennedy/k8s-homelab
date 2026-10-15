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

// The accessors below that take the workspace resolve the ci toolchain, which
// reads the workspace's Git history to decide whether the published devenv-ci
// image can stand in for a build (see Devenv.CiToolchain). They are cached for
// the session, so that the decision is made once per run, and made again on
// the next: a published image or a tag may appear between two runs with no
// file changing.

// Ci checks this repo's Dagger module, beyond what go's checks cover.
// +cache="session"
func (h *Homelab) Ci(ctx context.Context, ws dagger.Workspace) *Ci {
	return &Ci{Container: h.Toolchains.CiToolchain(ctx, ws)}
}

// Cue formats, upgrades, trims and exports the CUE configuration under config/.
// +cache="session"
func (h *Homelab) Cue(ctx context.Context, ws dagger.Workspace) *Cue {
	return &Cue{Container: h.Toolchains.CiToolchain(ctx, ws)}
}

// Go tests and lints every Go module in the repo.
// +cache="session"
func (h *Homelab) Go(ctx context.Context, ws dagger.Workspace) *Go {
	return &Go{Container: h.Toolchains.CiToolchain(ctx, ws)}
}

// Helmfile renders, lints and audits every chart with the repo's helmfile.
// +cache="session"
func (h *Homelab) Helmfile(
	ctx context.Context,
	ws dagger.Workspace,
	// Helmfile environments to check every chart in.
	// +optional
	// +default=["production"]
	environments []string,
) *Helmfile {
	return &Helmfile{Container: h.Toolchains.CiToolchain(ctx, ws), EnvironmentNames: environments}
}

// Kubernetes runs releases on throwaway or long-lived k3s clusters and tests
// them there. It runs in the ci and integration profiles, which no release
// publishes, so it is always built from source.
func (h *Homelab) Kubernetes() *Kubernetes {
	return &Kubernetes{Container: h.Toolchains.IntegrationToolchain()}
}

// Lab builds the lab CLI and keeps its Nix vendorHash current.
// +cache="session"
func (h *Homelab) Lab(ctx context.Context, ws dagger.Workspace) *Lab {
	return &Lab{Container: h.Toolchains.CiToolchain(ctx, ws), Nix: nixImage()}
}

// Nix formats and validates the Nix flake and host configuration.
// +cache="session"
func (h *Homelab) Nix(ctx context.Context, ws dagger.Workspace) *Nix {
	return &Nix{Container: h.Toolchains.CiToolchain(ctx, ws)}
}

// Python tests and formats every Python project in the repo.
// +cache="session"
func (h *Homelab) Python(ctx context.Context, ws dagger.Workspace) *Python {
	return &Python{Container: h.Toolchains.CiToolchain(ctx, ws)}
}

// Release builds, labels and publishes the repo's container images.
// +cache="session"
func (h *Homelab) Release(ctx context.Context, ws dagger.Workspace) *Release {
	return &Release{Container: h.Toolchains.CiToolchain(ctx, ws), Devenv: h.Toolchains}
}

// Terraform formats, lints, locks and validates the OpenTofu modules under terraform/.
// +cache="session"
func (h *Homelab) Terraform(
	ctx context.Context,
	ws dagger.Workspace,
	// Platforms the lock generator computes provider hashes for.
	// +optional
	// +default=["linux_arm64", "linux_amd64", "darwin_arm64"]
	platforms []string,
) *Terraform {
	return &Terraform{Container: h.Toolchains.CiToolchain(ctx, ws), Platforms: platforms}
}

// Woodpecker lints the Woodpecker CI pipelines.
// +cache="session"
func (h *Homelab) Woodpecker(ctx context.Context, ws dagger.Workspace) *Woodpecker {
	return &Woodpecker{Container: h.Toolchains.CiToolchain(ctx, ws)}
}

// Yaml lints every YAML file in the repo.
// +cache="session"
func (h *Homelab) Yaml(ctx context.Context, ws dagger.Workspace) *Yaml {
	return &Yaml{Container: h.Toolchains.CiToolchain(ctx, ws)}
}
