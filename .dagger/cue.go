package main

import (
	"bytes"
	"context"
	"dagger/homelab/internal/dagger"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// cueContainer returns a base CUE container with source mounted at /src,
// working directory set to /src/config (the CUE package root).
func cueContainer(source *dagger.Directory) *dagger.Container {
	return dag.Container().
		From(cueImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src/config")
}

// FormatCue formats CUE files with cue fmt.
// Returns a changeset. Use `dagger call format-cue --auto-apply` to apply.
// +generate
func (m *Homelab) FormatCue(
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) *dagger.Changeset {
	formatted := cueContainer(source).
		WithExec([]string{"cue", "fmt", "./..."}).
		Directory("/src")
	return formatted.Changes(source)
}

// FixCue upgrades CUE syntax to the current language version using cue fix.
// Returns a changeset. Use `dagger call fix-cue --auto-apply` to apply.
// +generate
func (m *Homelab) FixCue(
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) *dagger.Changeset {
	fixed := cueContainer(source).
		WithExec([]string{"cue", "fix", "./..."}).
		Directory("/src")
	return fixed.Changes(source)
}

// TrimCue removes redundant values implied by schema constraints using cue trim.
// Returns a changeset. Use `dagger call trim-cue --auto-apply` to apply.
// Advisory only — no corresponding check function.
// +generate
func (m *Homelab) TrimCue(
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) *dagger.Changeset {
	trimmed := cueContainer(source).
		WithExec([]string{"cue", "trim", "./..."}).
		Directory("/src")
	return trimmed.Changes(source)
}

// ExportCue exports each CUE environment to config/gen/<env>/env.json.
// Environments are discovered dynamically from the package's exported top-level values.
// Returns a changeset. Use `dagger call export-cue --auto-apply` to apply.
// +generate
func (m *Homelab) ExportCue(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue", "!config/gen/**"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	ctr := cueContainer(source)

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

// LintCue validates CUE formatting (cue fmt) and constraints (cue vet).
// Fails if any files need formatting or have constraint violations.
// Use `dagger call format-cue --auto-apply` to fix formatting.
// +check
func (m *Homelab) LintCue(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
	// +optional
	paths []string, //nolint:unparam // accepted for CI --changed uniformity (lab ci passes --paths to every +check); CUE lint always checks the whole tree
) (string, error) {
	// Check formatting via changeset.
	formatted := cueContainer(source).
		WithExec([]string{"cue", "fmt", "./..."}).
		Directory("/src")

	changeset := formatted.Changes(source)
	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking CUE formatting changes: %w", err)
	}

	if !empty {
		modified, _ := changeset.ModifiedPaths(ctx)
		return "", fmt.Errorf("CUE files need formatting: %s\nRun `dagger call format-cue --auto-apply` to fix", strings.Join(modified, ", "))
	}

	// Validate constraints.
	_, err = cueContainer(source).
		WithExec([]string{"cue", "vet", "./..."}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("cue vet failed: %w", err)
	}

	return "CUE lint passed", nil
}

// CheckFixCue validates that CUE files use current language syntax.
// Fails if cue fix would make any changes.
// Use `dagger call fix-cue --auto-apply` to apply fixes.
// +check
func (m *Homelab) CheckFixCue(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue"]
	source *dagger.Directory,
) (string, error) {
	changeset := m.FixCue(source)

	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking CUE fix changes: %w", err)
	}

	if !empty {
		modified, _ := changeset.ModifiedPaths(ctx)
		return "", fmt.Errorf("CUE files need syntax upgrades: %s\nRun `dagger call fix-cue --auto-apply` to fix", strings.Join(modified, ", "))
	}

	return "CUE syntax check passed", nil
}

// CheckExportCue validates that config/gen/<env>/env.json files are up to date.
// Fails if ExportCue would generate any changes.
// Use `dagger call export-cue --auto-apply` to regenerate.
// +check
func (m *Homelab) CheckExportCue(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!config/**/*.cue", "!config/gen/**"]
	source *dagger.Directory,
) (string, error) {
	changeset, err := m.ExportCue(ctx, source)
	if err != nil {
		return "", err
	}

	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking export changeset: %w", err)
	}

	if !empty {
		added, _ := changeset.AddedPaths(ctx)
		modified, _ := changeset.ModifiedPaths(ctx)
		var stale []string
		for _, p := range append(added, modified...) {
			if strings.HasSuffix(p, ".json") {
				stale = append(stale, p)
			}
		}
		return "", fmt.Errorf("config/gen files are stale: %s\nRun `dagger call export-cue --auto-apply` to fix", strings.Join(stale, ", "))
	}

	return "CUE export check passed", nil
}
