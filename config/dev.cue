package homelab

// dev is a single-node k3s cluster run by the Dagger module. It has no NixOS
// hosts, so `hosts` is empty and nothing provisions it.
//
// Releases are disabled by default. Enable one once it has integration tests
// under k8s/<tier>/<release>/tests.
dev: #Environment & _clusterDefaults & _appsDisabled & {
	name: "dev"

	cluster: {
		domain: "dev.localhost"
		networks: host_cidr: "172.19.0.0/16" // Distinct from staging's Kind network
	}

	hosts: []

	apps: foundation: {
		"cert-bundles":    true
		"cert-manager":    true
		"cert-system":     true
		"reflector":       true
		"secret-system":   true
		"storage-aliases": true
	}
}
