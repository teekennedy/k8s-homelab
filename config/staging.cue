package homelab

// staging is a Kind-based local development environment
staging: #Environment & _clusterDefaults & _stagingApps & {
	name: "staging"

	cluster: {
		domain: "staging.localhost"
		networks: host_cidr: "172.18.0.0/16" // Kind default network
	}

	// Staging uses a single-node Kind cluster
	hosts: [
		{
			name: "kind-control-plane"
			ip:   "172.18.0.2"
			k3s: {
				role:        "server"
				clusterInit: true
			}
		},
	]
}
