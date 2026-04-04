package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"
	"dagger/homelab/pathutil"
)

// Re-export pattern sets for use in main.go.
var (
	validateWoodpeckerPatterns = pathutil.ValidateWoodpeckerPatterns
)

// Delegate to pathutil for pure functions.
var (
	filterPaths         = pathutil.FilterPaths
	terraformModuleDirs = pathutil.TerraformModuleDirs
)

// findPythonProjects discovers Python project directories containing pyproject.toml.
// If paths is non-empty, returns only projects containing matching files.
// If paths is empty, returns all discovered projects.
func findPythonProjects(ctx context.Context, source *dagger.Directory, paths []string) ([]string, error) {
	out, err := dag.Container().
		From(alpineImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{
			"find", ".", "-name", "pyproject.toml",
			"-not", "-path", "*/.venv/*",
			"-not", "-path", "*/.dagger/*",
		}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding Python projects: %w", err)
	}

	var allDirs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			dir := filepath.Dir(strings.TrimPrefix(line, "./"))
			if dir != "." {
				allDirs = append(allDirs, dir)
			}
		}
	}
	sort.Strings(allDirs)

	return pathutil.MatchPythonProjects(paths, allDirs), nil
}

// findGoModules discovers Go module directories containing go.mod.
// If paths is non-empty, returns only modules containing matching files.
// If paths is empty, returns all discovered modules.
func findGoModules(ctx context.Context, source *dagger.Directory, paths []string) ([]string, error) {
	out, err := dag.Container().
		From(alpineImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"find", ".", "-name", "go.mod"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding Go modules: %w", err)
	}

	var allDirs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			dir := filepath.Dir(strings.TrimPrefix(line, "./"))
			allDirs = append(allDirs, dir)
		}
	}
	sort.Strings(allDirs)

	return pathutil.MatchGoModules(paths, allDirs), nil
}

// findHelmChartDirs discovers Helm chart directories matching the given paths.
// CUE config changes cause all charts to be returned (["k8s"]).
// If paths is empty, returns nil (caller should use default behavior).
func findHelmChartDirs(ctx context.Context, source *dagger.Directory, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	// Quick check: CUE changes don't need chart discovery
	for _, p := range paths {
		if strings.HasPrefix(p, "config/") && strings.HasSuffix(p, ".cue") {
			return []string{"k8s"}, nil
		}
	}

	k8sPaths := pathutil.FilterPaths(paths, []string{"k8s/**"})
	if len(k8sPaths) == 0 {
		return nil, nil
	}

	// Find all Chart.yaml files to determine chart boundaries
	out, err := dag.Container().
		From(alpineImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"find", "k8s", "-name", "Chart.yaml", "-not", "-path", "*/charts/*"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("finding Helm charts: %w", err)
	}

	chartDirs := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			chartDirs[filepath.Dir(line)] = true
		}
	}

	return pathutil.MatchHelmChartDirs(paths, chartDirs), nil
}
