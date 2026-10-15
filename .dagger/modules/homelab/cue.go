// Formats, upgrades, trims and exports the CUE configuration under config/.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"dagger/homelab/internal/dagger"
)

// Cue runs CUE generators over config/.
type Cue struct {
	// +private
	Container *dagger.Container
}

// cueContainer mounts source at /src in the toolchain container, with the
// working directory set to /src/config (the CUE package root).
func (m *Cue) cueContainer(source *dagger.Directory) *dagger.Container {
	container := m.Container
	return container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/config")
}

// Format formats CUE files with cue fmt.
// +generate
func (m *Cue) Format(
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) *dagger.Changeset {
	formatted := m.cueContainer(source).
		WithExec([]string{"cue", "fmt", "./..."}).
		Directory("/src")
	return formatted.Changes(source)
}

// Fix upgrades CUE syntax to the current language version using cue fix.
// +generate
func (m *Cue) Fix(
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) *dagger.Changeset {
	fixed := m.cueContainer(source).
		WithExec([]string{"cue", "fix", "./..."}).
		Directory("/src")
	return fixed.Changes(source)
}

// Trim removes redundant values implied by schema constraints using cue trim.
// +generate
func (m *Cue) Trim(
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) *dagger.Changeset {
	trimmed := m.cueContainer(source).
		WithExec([]string{"cue", "trim", "./..."}).
		Directory("/src")
	return trimmed.Changes(source)
}

// Export exports each CUE environment to config/gen/<env>/env.json.
// Environments are discovered dynamically from the package's exported top-level values.
// +generate
func (m *Cue) Export(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue", "!config/gen/**"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	ctr := m.cueContainer(source)

	allJSON, err := ctr.WithExec([]string{"cue", "export", "./..."}).Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("cue export: %w", err)
	}

	var envMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(allJSON), &envMap); err != nil {
		return nil, fmt.Errorf("parsing cue export output: %w", err)
	}

	// Sort env names for deterministic file generation order.
	envNames := make([]string, 0, len(envMap))
	for name := range envMap {
		envNames = append(envNames, name)
	}
	sort.Strings(envNames)

	for _, envName := range envNames {
		var buf bytes.Buffer
		if err := json.Indent(&buf, envMap[envName], "", "  "); err != nil {
			return nil, fmt.Errorf("indenting JSON for %s: %w", envName, err)
		}
		buf.WriteString("\n")

		ctr = ctr.WithNewFile(
			fmt.Sprintf("/src/config/gen/%s/env.json", envName),
			buf.String(),
		)
	}

	return ctr.Directory("/src").Changes(source), nil
}
