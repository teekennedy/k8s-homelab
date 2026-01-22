package homelab

// _clusterDefaults contains shared cluster defaults
_clusterDefaults: {
	cluster: {
		timezone: "America/Denver"
		networks: {
			pod_cidr:     "10.42.0.0/16"
			service_cidr: "10.43.0.0/16"
		}
	}
}

// _productionApps is the full application stack for production
_productionApps: {
	apps: {
		foundation: [
			"argocd",
			"cert-system",
			"cnpg-system",
			"external-dns",
			"kured",
			"kyverno",
			"longhorn-system",
			"metallb",
			"node-feature-discovery",
			"secret-system",
		]
		platform: [
			"auth-system",
			"gitea",
			"monitoring-system",
		]
		apps: [
			"homepage",
			"jellyfin",
		]
	}
}

// _stagingApps is a minimal application stack for staging
_stagingApps: {
	apps: {
		foundation: [
			"argocd",
			"cert-system",
			"metallb",
			"secret-system",
		]
		platform: [
			"auth-system",
			"gitea",
		]
		apps: [
			"homepage",
		]
	}
}
