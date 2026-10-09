package homelab

// ephemeral is a throwaway single-node cluster. It has no NixOS hosts, so
// `hosts` is empty and nothing provisions it.
//
// Releases are disabled by default. Enable one once it has integration tests
// under k8s/<tier>/<release>/tests.
ephemeral: #Environment & _clusterDefaults & _appsDisabled & {
	name: "ephemeral"

	cluster: {
		domain: "ephemeral.localhost"
		networks: host_cidr: "172.19.0.0/16" // Distinct from staging's Kind network
	}

	hosts: []

	apps: foundation: reflector: true
}
