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
		foundation: {
			"argocd":                   true
			"cert-system":              true
			"cnpg-system":              true
			"external-dns":             true
			"external-dns-internal":    true
			"generic-cdi-plugin":       true
			"intel-device-plugins-gpu": true
			"kured":                    true
			"kyverno":                  true
			"longhorn-system":          true
			"metallb":                  true
			"node-feature-discovery":   true
			"reflector":                true
			"s3-proxy":                 true
			"secret-system":            true
			"traefik":                  true
		}
		platform: {
			"auth-system":       true
			"crowdsec":          true
			"csi-driver-nfs":    true
			"csi-driver-smb":    true
			"dagger-engine":     true
			"gitea":             true
			"monitoring-system": true
			"oauth2-proxy":      true
			"renovate":          true
			"victoria-metrics":  true
			"woodpecker":        true
		}
		apps: {
			"homepage":  true
			"jellyfin":  true
			"spoolman":  true
			"terraria":  true
		}
	}
}

// _stagingApps is a minimal application stack for staging/ephemeral environments.
// All known releases are listed so --include-transitive-needs can resolve deps;
// set to false to exclude from the environment.
_stagingApps: {
	apps: {
		foundation: {
			"argocd":                   false
			"cert-system":              true
			"cnpg-system":              true
			"external-dns":             false
			"external-dns-internal":    false
			"generic-cdi-plugin":       false
			"intel-device-plugins-gpu": false
			"kured":                    false
			"kyverno":                  false
			"longhorn-system":          false
			"metallb":                  true
			"node-feature-discovery":   false
			"reflector":                false
			"s3-proxy":                 false
			"secret-system":            true
			"traefik":                  true
		}
		platform: {
			"auth-system":       true
			"crowdsec":          false
			"csi-driver-nfs":    false
			"csi-driver-smb":    false
			"dagger-engine":     false
			"gitea":             true
			"monitoring-system": false
			"oauth2-proxy":      false
			"renovate":          false
			"victoria-metrics":  false
			"woodpecker":        false
		}
		apps: {
			"homepage":  true
			"jellyfin":  false
			"spoolman":  false
			"terraria":  false
		}
	}
}
