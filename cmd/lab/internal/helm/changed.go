package helm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ChangedChartPaths returns chart paths that have changes relative to the given git ref.
// If gitRef is empty, defaults to HEAD.
func ChangedChartPaths(ctx context.Context, gitRef string) ([]string, error) {
	if gitRef == "" {
		gitRef = "HEAD"
	}

	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", gitRef)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	if hasCueConfigChange(lines) {
		return []string{"k8s"}, nil
	}

	return findChangedChartDirs(lines), nil
}

// hasCueConfigChange reports whether any changed line is a CUE config file,
// which can affect every chart's generated values.
func hasCueConfigChange(lines []string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "config/") && strings.HasSuffix(line, ".cue") {
			return true
		}
	}
	return false
}

// findChangedChartDirs returns the chart directories (nearest ancestor containing a
// Chart.yaml, excluding vendored charts/ subdirectories) for each changed k8s/ line.
func findChangedChartDirs(lines []string) []string {
	chartDirs := map[string]bool{}
	for _, line := range lines {
		if line == "" || !strings.HasPrefix(line, "k8s/") {
			continue
		}

		dir := filepath.Dir(line)
		for dir != "." && dir != "k8s" {
			chartYamlPath := filepath.Join(dir, "Chart.yaml")
			if _, err := os.Stat(chartYamlPath); err == nil {
				if !isInsideChartsDir(dir) {
					chartDirs[dir] = true
				}
				break
			}
			dir = filepath.Dir(dir)
		}
	}

	result := make([]string, 0, len(chartDirs))
	for dir := range chartDirs {
		result = append(result, dir)
	}
	return result
}

func isInsideChartsDir(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == "charts" {
			return true
		}
	}
	return false
}
