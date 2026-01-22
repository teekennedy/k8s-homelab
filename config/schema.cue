package homelab

// Regex patterns for validation
#CIDR:     =~"^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+/[0-9]+$"
#IPv4:     =~"^[0-9]+\\.[0-9]+\\.[0-9]+\\.[0-9]+$"
#Domain:   =~"^[a-z0-9][a-z0-9.-]*[a-z0-9]$"
#Hostname: =~"^[a-z][a-z0-9-]*[a-z0-9]$"
#Port:     uint & >0 & <=65535

// K3sRole defines valid k3s node roles
#K3sRole: "server" | "agent"

// Networks represents cluster network configuration
#Networks: {
	pod_cidr:     #CIDR
	service_cidr: #CIDR
	host_cidr:    #CIDR
}

// Cluster represents cluster-wide settings
#Cluster: {
	domain:   #Domain
	timezone: string | *"America/Denver"
	networks: #Networks
}

// K3sHost represents k3s-specific host configuration
#K3sHost: {
	role:         #K3sRole
	clusterInit?: bool
	serverAddr?:  string
}

// Host represents a NixOS host in the cluster
#Host: {
	name:     #Hostname
	ip:       #IPv4
	k3s:      #K3sHost
	modules?: [...string]
}

// Apps represents the application deployment configuration by tier
#Apps: {
	foundation: [...string]
	platform:   [...string]
	apps:       [...string]
}

// Environment represents a complete environment configuration
#Environment: {
	name:      string
	inherits?: string
	cluster:   #Cluster
	hosts:     [...#Host]
	apps:      #Apps

	// Validation: at least one host must have clusterInit if any hosts exist
	_hasClusterInit: or([for h in hosts if h.k3s.clusterInit == true {true}]) | len(hosts) == 0
}
