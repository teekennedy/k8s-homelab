package homelab

// production is the live homelab environment
production: #Environment & _clusterDefaults & _productionApps & {
	name: "production"

	cluster: {
		domain: "k8s.localhost"
		networks: host_cidr: "10.69.80.0/25"
	}

	hosts: [
		{
			name: "borg-0"
			ip:   "10.69.80.10"
			k3s: {
				role:       "server"
				serverAddr: "https://10.69.80.101:6443"
			}
		},
		{
			name: "borg-1"
			ip:   "10.69.80.11"
			k3s: {
				role:       "server"
				serverAddr: "https://10.69.80.101:6443"
			}
		},
		{
			name: "borg-2"
			ip:   "10.69.80.12"
			k3s: {
				role:        "server"
				clusterInit: true
			}
		},
		{
			name: "borg-3"
			ip:   "10.69.80.13"
			k3s: {
				role:       "agent"
				serverAddr: "https://10.69.80.101:6443"
			}
		},
	]
}
