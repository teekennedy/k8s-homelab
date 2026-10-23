// Builds, labels and publishes the repo's container images, as listed in
// releases.json, and tests the release scripts that tag them.
//
// The devenv images are built by the nix module. Its remote builders are
// configured on that module, so this one takes the same settings and passes
// them on.
package main

import (
	"dagger/release/internal/dagger"
)

// Release builds and publishes the repo's container images.
type Release struct {
	// +private
	Container *dagger.Container
	// +private
	NixBuilders string
	// +private
	SSHAuthSock *dagger.Socket
	// +private
	SSHConfig *dagger.File
	// +private
	SSHUser string
	// +private
	SSHKnownHosts *dagger.File
}

// Builds, labels and publishes the repo's container images.
func New(
	// Container to run the release script tests in. dagger.toml wires in the
	// nix module's ci-toolchain.
	container *dagger.Container,
	// The nix module's nixBuilders, for the devenv image builds.
	// +optional
	nixBuilders string,
	// The nix module's sshAuthSock.
	// +optional
	sshAuthSock *dagger.Socket,
	// The nix module's sshConfig.
	// +optional
	sshConfig *dagger.File,
	// The nix module's sshUser.
	// +optional
	sshUser string,
	// The nix module's sshKnownHosts.
	// +defaultPath="/k8s/platform/woodpecker/files/known_hosts"
	sshKnownHosts *dagger.File,
) *Release {
	return &Release{
		Container:     container,
		NixBuilders:   nixBuilders,
		SSHAuthSock:   sshAuthSock,
		SSHConfig:     sshConfig,
		SSHUser:       sshUser,
		SSHKnownHosts: sshKnownHosts,
	}
}

// nix is the nix module, with this module's builder settings.
func (m *Release) nix() *dagger.Nix {
	return dag.Nix(dagger.NixOpts{
		NixBuilders:   m.NixBuilders,
		SSHAuthSock:   m.SSHAuthSock,
		SSHConfig:     m.SSHConfig,
		SSHUser:       m.SSHUser,
		SSHKnownHosts: m.SSHKnownHosts,
	})
}
