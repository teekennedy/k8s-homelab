// Package helmfile holds what the helmfile and kubernetes toolchains both know
// about this repo's helmfile state: where its files live, and how to read
// `helmfile list` and an environment's generated values. It is plain Go with no
// Dagger types, because each toolchain generates its own SDK.
package helmfile

import (
	"encoding/json"
	"fmt"
	"path"
)

const (
	// StatePath is the repo-relative helmfile state file.
	StatePath = "helmfile.yaml.gotmpl"
	// SharedChartsPath is the repo-relative directory holding library charts.
	SharedChartsPath = "k8s/charts"
)

// EnvValuesPath is the generated values file helmfile loads for env.
func EnvValuesPath(env string) string {
	return path.Join("config/gen", env, "env.json")
}

// Release is one release in `helmfile list --output json`.
type Release struct {
	Name string `json:"name"`
	// Namespace is where helmfile deploys the release. Templated per release in
	// the state file, so it is read from helmfile rather than guessed.
	Namespace string `json:"namespace"`
	Chart     string `json:"chart"`
}

// ParseList parses `helmfile list --output json`.
func ParseList(listJSON string) ([]Release, error) {
	var releases []Release
	if err := json.Unmarshal([]byte(listJSON), &releases); err != nil {
		return nil, fmt.Errorf("parsing helmfile list output: %w", err)
	}
	return releases, nil
}

// ParseEnvApps returns an environment's app list from its generated values,
// keyed by tier then release name. This is what the state file's
// installedTemplate reads to decide whether a release is installed.
func ParseEnvApps(envJSON string) (map[string]map[string]bool, error) {
	var values struct {
		Apps map[string]map[string]bool `json:"apps"`
	}
	if err := json.Unmarshal([]byte(envJSON), &values); err != nil {
		return nil, fmt.Errorf("parsing environment values: %w", err)
	}
	return values.Apps, nil
}
