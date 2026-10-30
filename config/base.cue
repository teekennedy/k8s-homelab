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
			"cert-bundles":             true
			"cert-manager":             true
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
			"storage-aliases":          false
			"traefik":                  true
		}
		platform: {
			"auth-system":       true
			"crowdsec":          true
			"csi-driver-nfs":    true
			"csi-driver-smb":    true
			"dagger-engine":     true
			"forgejo":           true
			"monitoring-system": true
			"oauth2-proxy":      true
			"renovate":          true
			"restic":            true
			"victoria-metrics":  true
			"woodpecker":        true
		}
		apps: {
			"copyparty": true
			"freshrss":  true
			"gauntlet":  true
			"homepage":  true
			"jellyfin":  true
			"joplin":    true
			"openhands": true
			"redteam":   true
			"romm":      true
			"spoolman":  true
			"syncthing": true
			"terraria":  true
		}
	}
}

// _appsDisabled defaults every known release to disabled, so an environment that
// wants only a handful of releases can name those and leave the rest alone.
//
// The names come from _productionApps rather than being listed again: that is
// the full stack, and LintHelmfile already fails when a release is missing from
// it, so it is the one list guaranteed to be complete. The values are defaults
// (`*false`), which is what lets an environment unify `true` over one of them.
_appsDisabled: {
	apps: {
		for tier, releases in _productionApps.apps {
			(tier): {
				for name, _ in releases {
					(name): *false | bool
				}
			}
		}
	}
}

// _stagingApps is a minimal application stack for staging/dev environments.
// Every known release stays listed, disabled, so --include-transitive-needs can
// resolve deps.
_stagingApps: _appsDisabled & {
	apps: {
		foundation: {
			"cert-bundles":    true
			"cert-manager":    true
			"cert-system":     true
			"cnpg-system":     true
			"metallb":         true
			"secret-system":   true
			"storage-aliases": true
			"traefik":         true
		}
		platform: {
			"auth-system": true
			"forgejo":     true
		}
		apps: "homepage": true
	}
}
