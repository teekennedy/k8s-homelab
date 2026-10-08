package main

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"
)

// helmRepoRoot is where the scoped repo layout is mounted in helmfile containers.
const helmRepoRoot = "/repo"

// sharedChartsPath is the repo-relative directory holding library charts.
const sharedChartsPath = "k8s/charts"

// discoverHelmChartPaths finds all Helm chart directories in source.
func discoverHelmChartPaths(ctx context.Context, source *dagger.Directory) []string {
	chartFiles, _ := source.Glob(ctx, "k8s/**/Chart.yaml")
	var paths []string
	for _, f := range chartFiles {
		if !strings.Contains(f, "/charts/") {
			paths = append(paths, filepath.Dir(f))
		}
	}
	sort.Strings(paths)
	return paths
}

// matchChartPaths returns chart paths that contain any of the given file paths.
// CUE config changes cause all charts to be returned.
func matchChartPaths(filePaths, chartPaths []string) []string {
	// CUE changes affect all charts
	for _, p := range filePaths {
		if strings.HasPrefix(p, "config/") && strings.HasSuffix(p, ".cue") {
			return chartPaths
		}
	}

	matched := map[string]bool{}
	for _, p := range filePaths {
		for _, dir := range chartPaths {
			if strings.HasPrefix(p, dir+"/") || p == dir {
				matched[dir] = true
			}
		}
	}

	var result []string
	for _, dir := range chartPaths {
		if matched[dir] {
			result = append(result, dir)
		}
	}
	return result
}
