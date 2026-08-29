package homelab

// ephemeral is a short-lived, per-branch/PR Kind-based environment.
// Not provisioned yet; exists so helmfile can template against it without error.
ephemeral: #Environment & _clusterDefaults & _stagingApps & {
	name: "ephemeral"

	cluster: {
		domain: "ephemeral.localhost"
		networks: host_cidr: "172.19.0.0/16" // Distinct from staging's Kind network
	}

	// Ephemeral uses a single-node Kind cluster, same as staging.
	hosts: [
		{
			name: "kind-control-plane"
			ip:   "172.19.0.2"
			k3s: {
				role:        "server"
				clusterInit: true
			}
		},
	]
}
