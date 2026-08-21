package helm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type ChartInfo struct {
	Path        string
	Name        string
	Tier        string
	Namespace   string
	ReleaseName string
}

type chartYaml struct {
	Dependencies []chartDependency `yaml:"dependencies"`
}

type chartDependency struct {
	Name       string `yaml:"name"`
	Version    string `yaml:"version"`
	Repository string `yaml:"repository"`
	Condition  string `yaml:"condition"`
}

type applicationSpec struct {
	Spec struct {
		Destination struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"destination"`
		Source struct {
			Helm struct {
				ReleaseName string `yaml:"releaseName"`
			} `yaml:"helm"`
		} `yaml:"source"`
	} `yaml:"spec"`
}

// DiscoverCharts finds all Helm charts under the given base directory.
func DiscoverCharts(basePath string) ([]ChartInfo, error) {
	var charts []ChartInfo

	err := filepath.WalkDir(basePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "Chart.yaml" {
			return nil
		}

		dir := filepath.Dir(path)

		for _, segment := range strings.Split(filepath.ToSlash(dir), "/") {
			if segment == "charts" {
				return nil
			}
		}

		info, err := ParseChartInfo(dir)
		if err != nil {
			return fmt.Errorf("parse chart %s: %w", dir, err)
		}

		charts = append(charts, info)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", basePath, err)
	}

	return charts, nil
}

// ParseChartInfo extracts chart metadata from the directory.
func ParseChartInfo(chartDir string) (ChartInfo, error) {
	info := ChartInfo{
		Path: chartDir,
		Name: filepath.Base(chartDir),
	}

	parts := strings.Split(filepath.ToSlash(chartDir), "/")
	for i, p := range parts {
		if p == "k8s" && i+1 < len(parts) {
			info.Tier = parts[i+1]
			break
		}
	}

	info.ReleaseName = info.Name

	appYaml := filepath.Join(chartDir, "application.yaml")
	data, err := os.ReadFile(appYaml) //nolint:gosec // chartDir is an internal repo-relative path, not user input
	if err != nil {
		if os.IsNotExist(err) {
			info.Namespace = info.Name
			return info, nil
		}
		return info, fmt.Errorf("read application.yaml: %w", err)
	}

	var app applicationSpec
	if err := yaml.Unmarshal(data, &app); err != nil {
		return info, fmt.Errorf("parse application.yaml: %w", err)
	}

	info.Namespace = app.Spec.Destination.Namespace
	if info.Namespace == "" {
		info.Namespace = info.Name
	}

	if app.Spec.Source.Helm.ReleaseName != "" {
		info.ReleaseName = app.Spec.Source.Helm.ReleaseName
	}

	return info, nil
}

// NeedsDependencyBuild checks if a chart's dependencies need to be downloaded or updated.
// Returns true if:
//   - Chart.yaml declares dependencies and the charts/ directory doesn't exist
//   - Chart.yaml declares dependencies that have no matching .tgz in charts/
//   - A Chart.lock exists and is newer than the newest .tgz file
//   - A .tgz exists that doesn't match any declared dependency (stale artifact)
func NeedsDependencyBuild(chartDir string) (bool, error) {
	deps, err := readChartDependencies(chartDir)
	if err != nil {
		return false, err
	}
	if len(deps) == 0 {
		return false, nil
	}

	chartsDir := filepath.Join(chartDir, "charts")
	tgzFiles, newestTgz, err := scanTgzFiles(chartsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("read charts dir: %w", err)
	}

	if depsMissingOrStaleTgz(deps, tgzFiles) {
		return true, nil
	}

	lockPath := filepath.Join(chartDir, "Chart.lock")
	lockInfo, err := os.Stat(lockPath)
	if err == nil && !newestTgz.IsZero() && lockInfo.ModTime().After(newestTgz) {
		return true, nil
	}

	return false, nil
}

// readChartDependencies reads and parses the dependencies declared in chartDir's Chart.yaml.
func readChartDependencies(chartDir string) ([]chartDependency, error) {
	chartYamlPath := filepath.Join(chartDir, "Chart.yaml")
	data, err := os.ReadFile(chartYamlPath) //nolint:gosec // chartDir is an internal repo-relative path, not user input
	if err != nil {
		return nil, fmt.Errorf("read Chart.yaml: %w", err)
	}

	var chart chartYaml
	if err := yaml.Unmarshal(data, &chart); err != nil {
		return nil, fmt.Errorf("parse Chart.yaml: %w", err)
	}
	return chart.Dependencies, nil
}

// scanTgzFiles returns the .tgz filenames present in chartsDir and the most recent
// modification time among them.
func scanTgzFiles(chartsDir string) (map[string]bool, time.Time, error) {
	entries, err := os.ReadDir(chartsDir)
	if err != nil {
		return nil, time.Time{}, err //nolint:wrapcheck // returned as-is so callers can still check os.IsNotExist(err)
	}

	tgzFiles := map[string]bool{}
	var newestTgz time.Time
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tgz") {
			tgzFiles[e.Name()] = true
			info, err := e.Info()
			if err == nil && info.ModTime().After(newestTgz) {
				newestTgz = info.ModTime()
			}
		}
	}
	return tgzFiles, newestTgz, nil
}

// depsMissingOrStaleTgz reports whether deps has any dependency without a matching .tgz
// in tgzFiles, or whether tgzFiles has any leftover .tgz not matching a declared dependency.
// tgzFiles is mutated: matched entries are removed.
func depsMissingOrStaleTgz(deps []chartDependency, tgzFiles map[string]bool) bool {
	for _, dep := range deps {
		expectedTgz := dep.Name + "-" + dep.Version + ".tgz"
		if !tgzFiles[expectedTgz] {
			return true
		}
		delete(tgzFiles, expectedTgz)
	}
	return len(tgzFiles) > 0
}
