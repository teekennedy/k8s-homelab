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

// Kubernetes runs releases on k3s clusters.
type Kubernetes struct {
	// +private
	Container *dagger.Container
}

// Runs releases on throwaway or long-lived k3s clusters and tests them there.
func New(
	// Container to run helmfile, kubectl and pytest in. dagger.toml wires in
	// devenv's integration-toolchain: the ci profile plus kubectl and curl.
	container *dagger.Container,
) *Kubernetes {
	return &Kubernetes{Container: container}
}
