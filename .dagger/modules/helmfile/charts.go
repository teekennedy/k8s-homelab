package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/helmfile/internal/dagger"
)

// helmRepoRoot is where the scoped repo layout is mounted in helmfile containers.
const helmRepoRoot = "/repo"

// discoverHelmChartPaths finds all Helm chart directories in source.
func discoverHelmChartPaths(ctx context.Context, source *dagger.Directory) ([]string, error) {
	chartFiles, err := source.Glob(ctx, "k8s/**/Chart.yaml")
	if err != nil {
		return nil, fmt.Errorf("discovering charts: %w", err)
	}
	var paths []string
	for _, f := range chartFiles {
		if !strings.Contains(f, "/charts/") {
			paths = append(paths, filepath.Dir(f))
		}
	}
	sort.Strings(paths)
	return paths, nil
}
