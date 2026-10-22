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
	// +private
	Builders *NixBuilders
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
	// Remote builders for the Nix and devenv builds, in the format of Nix's
	// `builders` setting: one machine per line or per ";", such as the
	// contents of /etc/nix/machines. Only ssh-ng:// and ssh:// machines are
	// used, and they authenticate with sshAuthSock alone. Builds fall back to
	// the container when no builder answers.
	// +optional
	nixBuilders string,
	// The SSH agent the builders authenticate with, as unix:///path/to/socket.
	// Only the agent is forwarded; no key leaves the host.
	// +optional
	sshAuthSock *dagger.Socket,
	// An ssh_config for reaching the builders, such as ~/.ssh/config. Its
	// IdentityFile, known_hosts and connection-sharing options are overridden.
	// +optional
	sshConfig *dagger.File,
	// The user to log in to the builders as, where neither their machine URI
	// nor sshConfig names one.
	// +optional
	sshUser string,
	// The builders' SSH host keys.
	// +defaultPath="/k8s/platform/woodpecker/files/known_hosts"
	sshKnownHosts *dagger.File,
) *Homelab {
	builders := newNixBuilders(nixBuilders, sshAuthSock, sshConfig, sshUser, sshKnownHosts)
	return &Homelab{
		Toolchains: &Devenv{Source: devenvSource, ToolchainSource: toolchainSource, Builders: builders},
		Builders:   builders,
	}
}

// NixBuilders reports on the remote Nix builders the Nix and devenv builds use.
func (h *Homelab) NixBuilders() *NixBuilders {
	return h.Builders
}

// Devenv builds the devenv shell as a container: the toolchain the checks run
// in, and the devenv images releases publish.
func (h *Homelab) Devenv() *Devenv {
	return h.Toolchains
}

// Ci checks this repo's Dagger module, beyond what go's checks cover.
func (h *Homelab) Ci(ctx context.Context) (*Ci, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Ci{Container: container}, nil
}

// Cue formats, upgrades, trims and exports the CUE configuration under config/.
func (h *Homelab) Cue(ctx context.Context) (*Cue, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Cue{Container: container}, nil
}

// Go tests and lints every Go module in the repo.
func (h *Homelab) Go(ctx context.Context) (*Go, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Go{Container: container}, nil
}

// Helmfile renders, lints and audits every chart with the repo's helmfile.
func (h *Homelab) Helmfile(
	ctx context.Context,
	// Helmfile environments to check every chart in.
	// +optional
	// +default=["production"]
	environments []string,
) (*Helmfile, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Helmfile{Container: container, EnvironmentNames: environments}, nil
}

// Kubernetes runs releases on throwaway or long-lived k3s clusters and tests
// them there. It runs in the ci and integration profiles, which no release
// publishes, so it is always built from source.
func (h *Homelab) Kubernetes(ctx context.Context) (*Kubernetes, error) {
	container, err := h.Toolchains.IntegrationToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Kubernetes{Container: container}, nil
}

// Lab builds the lab CLI and keeps its Nix vendorHash current.
func (h *Homelab) Lab(ctx context.Context) (*Lab, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Lab{Container: container, Builders: h.Builders}, nil
}

// Nix formats and validates the Nix flake and host configuration.
func (h *Homelab) Nix(ctx context.Context) (*Nix, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Nix{Container: container, Builders: h.Builders}, nil
}

// Python tests and formats every Python project in the repo.
func (h *Homelab) Python(ctx context.Context) (*Python, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Python{Container: container}, nil
}

// Release builds, labels and publishes the repo's container images.
func (h *Homelab) Release(ctx context.Context) (*Release, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Release{Container: container, Devenv: h.Toolchains}, nil
}

// Terraform formats, lints, locks and validates the OpenTofu modules under terraform/.
func (h *Homelab) Terraform(
	ctx context.Context,
	// Platforms the lock generator computes provider hashes for.
	// +optional
	// +default=["linux_arm64", "linux_amd64", "darwin_arm64"]
	platforms []string,
) (*Terraform, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Terraform{Container: container, Platforms: platforms}, nil
}

// Woodpecker lints the Woodpecker CI pipelines.
func (h *Homelab) Woodpecker(ctx context.Context) (*Woodpecker, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Woodpecker{Container: container}, nil
}

// Yaml lints every YAML file in the repo.
func (h *Homelab) Yaml(ctx context.Context) (*Yaml, error) {
	container, err := h.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Yaml{Container: container}, nil
}
