// Throwaway and long-lived k3s clusters, and the integration workflow that
// deploys releases onto one with the repo's own helmfile and runs their pytest
// suites against it.
//
// Nothing here is a check: a k3s server needs an engine that allows privileged
// execs, and `dagger check` runs whatever a PR contains. See "Kubernetes
// integration tests" in README.md.
package main

import (
	"dagger/kubernetes/internal/dagger"
)

// integrationProfiles adds the cluster tooling the workflow drives — kubectl
// and curl — on top of the ci toolchain.
var integrationProfiles = []string{"ci", "integration"}

// Kubernetes runs releases on k3s clusters.
type Kubernetes struct {
	// +private
	Container *dagger.Container
}

// Runs releases on throwaway or long-lived k3s clusters and tests them there.
func New(
	// Container to run helmfile, kubectl and pytest in; defaults to the devenv
	// ci and integration profiles.
	// +optional
	container *dagger.Container,
) *Kubernetes {
	return &Kubernetes{Container: container}
}

func (m *Kubernetes) toolchain() *dagger.Container {
	if m.Container != nil {
		return m.Container
	}
	return dag.Devenv().Toolchain(dagger.DevenvToolchainOpts{Profiles: integrationProfiles})
}
