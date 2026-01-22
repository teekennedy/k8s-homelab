package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
)

// LoadEnvironment loads and resolves a CUE environment configuration
func LoadEnvironment(configDir, envName string) (*Environment, error) {
	ctx := cuecontext.New()

	// Load all CUE files from the config directory
	cfg := &load.Config{
		Dir: configDir,
	}

	instances := load.Instances([]string{"."}, cfg)
	if len(instances) == 0 {
		return nil, fmt.Errorf("no CUE instances found in %s", configDir)
	}

	inst := instances[0]
	if inst.Err != nil {
		return nil, fmt.Errorf("load CUE instance: %w", inst.Err)
	}

	value := ctx.BuildInstance(inst)
	if value.Err() != nil {
		return nil, fmt.Errorf("build CUE instance: %w", value.Err())
	}

	// Look up the environment by name
	envValue := value.LookupPath(cue.ParsePath(envName))
	if !envValue.Exists() {
		return nil, fmt.Errorf("environment %q not found in configuration", envName)
	}

	// Decode into our Go struct
	var env Environment
	if err := envValue.Decode(&env); err != nil {
		return nil, fmt.Errorf("decode environment: %w", err)
	}

	return &env, nil
}

// ValidateEnvironment validates a CUE environment configuration
func ValidateEnvironment(configDir, envName string) error {
	ctx := cuecontext.New()

	cfg := &load.Config{
		Dir: configDir,
	}

	instances := load.Instances([]string{"."}, cfg)
	if len(instances) == 0 {
		return fmt.Errorf("no CUE instances found in %s", configDir)
	}

	inst := instances[0]
	if inst.Err != nil {
		return fmt.Errorf("load CUE instance: %w", inst.Err)
	}

	value := ctx.BuildInstance(inst)
	if value.Err() != nil {
		return fmt.Errorf("build CUE instance: %w", value.Err())
	}

	// Look up the environment
	envValue := value.LookupPath(cue.ParsePath(envName))
	if !envValue.Exists() {
		return fmt.Errorf("environment %q not found", envName)
	}

	// Validate against the schema
	schemaValue := value.LookupPath(cue.ParsePath("#Environment"))
	if schemaValue.Exists() {
		unified := schemaValue.Unify(envValue)
		if err := unified.Validate(cue.Concrete(true)); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	// Check for concrete values
	if err := envValue.Validate(cue.Concrete(true)); err != nil {
		return fmt.Errorf("incomplete configuration: %w", err)
	}

	return nil
}

// ExportEnvironment exports the environment configuration to different formats
func ExportEnvironment(configDir, envName, format string) (string, error) {
	env, err := LoadEnvironment(configDir, envName)
	if err != nil {
		return "", err
	}

	switch format {
	case "json":
		return exportJSON(env)
	case "yaml":
		return exportYAML(env)
	case "nix":
		return exportNix(env)
	case "helm":
		return exportHelm(env)
	case "terraform", "tf":
		return exportTerraform(env)
	default:
		return "", fmt.Errorf("unsupported format: %s (supported: json, yaml, nix, helm, terraform)", format)
	}
}

func exportJSON(env *Environment) (string, error) {
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

func exportYAML(env *Environment) (string, error) {
	// Simple YAML export - for production, use gopkg.in/yaml.v3
	jsonBytes, err := json.Marshal(env)
	if err != nil {
		return "", err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		return "", err
	}

	return toYAML(data, 0), nil
}

func toYAML(data interface{}, indent int) string {
	prefix := ""
	for i := 0; i < indent; i++ {
		prefix += "  "
	}

	switch v := data.(type) {
	case map[string]interface{}:
		result := ""
		for key, val := range v {
			switch child := val.(type) {
			case map[string]interface{}:
				result += fmt.Sprintf("%s%s:\n%s", prefix, key, toYAML(child, indent+1))
			case []interface{}:
				result += fmt.Sprintf("%s%s:\n", prefix, key)
				for _, item := range child {
					if m, ok := item.(map[string]interface{}); ok {
						result += fmt.Sprintf("%s  -\n%s", prefix, toYAML(m, indent+2))
					} else {
						result += fmt.Sprintf("%s  - %v\n", prefix, item)
					}
				}
			default:
				result += fmt.Sprintf("%s%s: %v\n", prefix, key, val)
			}
		}
		return result
	default:
		return fmt.Sprintf("%v", v)
	}
}

func exportNix(env *Environment) (string, error) {
	// Generate Nix expression for host configurations
	result := fmt.Sprintf(`# Generated from CUE environment: %s
# Do not edit directly - regenerate with: lab config export %s nix
{
  environment = {
    name = "%s";
    domain = "%s";
    timezone = "%s";
  };

  networks = {
    hostCidr = "%s";
    podCidr = "%s";
    serviceCidr = "%s";
  };

  hosts = {
`, env.Name, env.Name, env.Name, env.Cluster.Domain, env.Cluster.Timezone,
		env.Cluster.Networks.HostCIDR, env.Cluster.Networks.PodCIDR, env.Cluster.Networks.ServiceCIDR)

	for _, h := range env.Hosts {
		result += fmt.Sprintf(`    %s = {
      ip = "%s";
      k3s = {
        role = "%s";
`, h.Name, h.IP, h.K3s.Role)
		if h.K3s.ClusterInit {
			result += "        clusterInit = true;\n"
		}
		if h.K3s.ServerAddr != "" {
			result += fmt.Sprintf("        serverAddr = \"%s\";\n", h.K3s.ServerAddr)
		}
		result += "      };\n"
		if len(h.Modules) > 0 {
			result += "      modules = [\n"
			for _, m := range h.Modules {
				result += fmt.Sprintf("        \"%s\"\n", m)
			}
			result += "      ];\n"
		}
		result += "    };\n"
	}

	result += `  };
}
`
	return result, nil
}

func exportHelm(env *Environment) (string, error) {
	// Generate Helm values for cluster-wide settings
	return fmt.Sprintf(`# Generated from CUE environment: %s
# Do not edit directly - regenerate with: lab config export %s helm

global:
  domain: %s
  timezone: %s

network:
  hostCidr: %s
  podCidr: %s
  serviceCidr: %s
`, env.Name, env.Name, env.Cluster.Domain, env.Cluster.Timezone,
		env.Cluster.Networks.HostCIDR, env.Cluster.Networks.PodCIDR, env.Cluster.Networks.ServiceCIDR), nil
}

func exportTerraform(env *Environment) (string, error) {
	// Generate Terraform tfvars
	result := fmt.Sprintf(`# Generated from CUE environment: %s
# Do not edit directly - regenerate with: lab config export %s terraform

environment = "%s"
domain      = "%s"
timezone    = "%s"

network = {
  host_cidr    = "%s"
  pod_cidr     = "%s"
  service_cidr = "%s"
}

hosts = {
`, env.Name, env.Name, env.Name, env.Cluster.Domain, env.Cluster.Timezone,
		env.Cluster.Networks.HostCIDR, env.Cluster.Networks.PodCIDR, env.Cluster.Networks.ServiceCIDR)

	for _, h := range env.Hosts {
		result += fmt.Sprintf(`  %s = {
    ip          = "%s"
    k3s_role    = "%s"
`, h.Name, h.IP, h.K3s.Role)
		if h.K3s.ClusterInit {
			result += "    cluster_init = true\n"
		}
		if h.K3s.ServerAddr != "" {
			result += fmt.Sprintf("    server_addr = \"%s\"\n", h.K3s.ServerAddr)
		}
		result += "  }\n"
	}

	result += "}\n"
	return result, nil
}

// GetProjectRoot attempts to find the project root by looking for marker files
func GetProjectRoot() (string, error) {
	markers := []string{"flake.nix", "dagger.json", ".git"}
	dir, err := filepath.Abs(".")
	if err != nil {
		return "", err
	}

	for {
		for _, marker := range markers {
			if _, err := filepath.Glob(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("could not find project root (no flake.nix, dagger.json, or .git found)")
}
