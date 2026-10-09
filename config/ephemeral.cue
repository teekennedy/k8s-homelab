package homelab

// ephemeral is the throwaway single-node k3s cluster that the Dagger Kubernetes
// integration workflow creates and destroys around a test run (see
// .dagger/k3s.go). It has no NixOS hosts: the node is a Dagger service, so
// `hosts` is empty and nothing provisions it.
//
// Releases are disabled by default here. Enable one once it has integration
// tests under k8s/<tier>/<release>/tests — that is what the workflow deploys.
ephemeral: #Environment & _clusterDefaults & _appsDisabled & {
	name: "ephemeral"

	cluster: {
		domain: "ephemeral.localhost"
		networks: host_cidr: "172.19.0.0/16" // Distinct from staging's Kind network
	}

	hosts: []

	apps: foundation: reflector: true
}
