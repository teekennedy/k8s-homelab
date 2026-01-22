package config

// Environment represents a complete environment configuration
type Environment struct {
	Name     string  `json:"name"`
	Inherits string  `json:"inherits,omitempty"`
	Cluster  Cluster `json:"cluster"`
	Hosts    []Host  `json:"hosts"`
	Apps     Apps    `json:"apps"`
}

// Cluster represents cluster-wide settings
type Cluster struct {
	Domain   string   `json:"domain"`
	Timezone string   `json:"timezone"`
	Networks Networks `json:"networks"`
}

// Networks represents network configuration
type Networks struct {
	PodCIDR     string `json:"pod_cidr"`
	ServiceCIDR string `json:"service_cidr"`
	HostCIDR    string `json:"host_cidr"`
}

// Host represents a NixOS host in the cluster
type Host struct {
	Name    string   `json:"name"`
	IP      string   `json:"ip"`
	K3s     K3sHost  `json:"k3s"`
	Modules []string `json:"modules,omitempty"`
}

// K3sHost represents k3s-specific host configuration
type K3sHost struct {
	Role        string `json:"role"`
	ClusterInit bool   `json:"clusterInit,omitempty"`
	ServerAddr  string `json:"serverAddr,omitempty"`
}

// Apps represents the application deployment configuration
type Apps struct {
	Foundation []string `json:"foundation"`
	Platform   []string `json:"platform"`
	Apps       []string `json:"apps"`
}
