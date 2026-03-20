package helm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ChangedChartPaths returns chart paths that have changes relative to the given git ref.
// If gitRef is empty, defaults to HEAD.
func ChangedChartPaths(gitRef string) ([]string, error) {
	if gitRef == "" {
		gitRef = "HEAD"
	}

	cmd := exec.Command("git", "diff", "--name-only", gitRef)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "config/") && strings.HasSuffix(line, ".cue") {
			return []string{"k8s"}, nil
		}
	}

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
	return result, nil
}

func isInsideChartsDir(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == "charts" {
			return true
		}
	}
	return false
}
