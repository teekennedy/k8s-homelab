// Everything this repo builds with Nix: the flake and host configuration's
// checks, the devenv toolchain every other module's checks run in, and the lab
// CLI. They share one set of remote builders and one Nix store cache.
//
// The other modules take their toolchain as a constructor argument, which
// dagger.toml wires to `dag://nix/devenv/ci-toolchain`.
package main

import (
	"context"

	"dagger/nix/internal/dagger"
)

// Nix formats, validates and builds the repo's Nix code and devenv toolchain.
type Nix struct {
	// +private
	Toolchains *Devenv
	// +private
	Remote *Builders
}

// Formats, validates and builds the repo's Nix code and devenv toolchain.
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
) *Nix {
	builders := newBuilders(nixBuilders, sshAuthSock, sshConfig, sshUser, sshKnownHosts)
	return &Nix{
		Toolchains: &Devenv{Source: devenvSource, ToolchainSource: toolchainSource, Builders: builders},
		Remote:     builders,
	}
}

// Builders reports on the remote Nix builders the Nix and devenv builds use.
func (m *Nix) Builders() *Builders {
	return m.Remote
}

// Devenv builds the devenv shell as a container: the toolchain the checks run
// in, and the devenv images releases publish.
func (m *Nix) Devenv() *Devenv {
	return m.Toolchains
}

// Lab builds the lab CLI and keeps its Nix vendorHash current.
func (m *Nix) Lab(ctx context.Context) (*Lab, error) {
	container, err := m.Toolchains.CiToolchain(ctx)
	if err != nil {
		return nil, err
	}
	return &Lab{Container: container, Builders: m.Remote}, nil
}
