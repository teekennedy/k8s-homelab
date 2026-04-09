package main

import (
	"context"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

//go:embed scripts/helm-deps.sh
var helmDepsScript string

//go:embed scripts/helm-template.sh
var helmTemplateScript string

//go:embed scripts/helm-lint.sh
var helmLintScript string

// HelmChart is a Helm chart with a scoped source directory.
// Each HelmChart carries only the files for its chart, enabling
// per-chart caching: changing files in one chart won't invalidate
// the cache for other charts.
type HelmChart struct {
	// Path is the chart's directory relative to the repo root
	// (e.g. "k8s/apps/jellyfin").
	Path string
	// Source is the chart's scoped source directory.
	Source *dagger.Directory
	// ClusterValues is the optional cluster-values.yaml for template rendering.
	// +private
	ClusterValues *dagger.File
}

// HelmCharts returns all discovered Helm charts with scoped source directories.
// Each chart's Source is a subdirectory of the +defaultPath source, so
// Directory IDs are stable across sessions and cache independently.
func (m *Homelab) HelmCharts(
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
) []*HelmChart {
	var charts []*HelmChart

	// Check for cluster-values.yaml (used by template rendering)
	clusterValues := source.File("config/gen/cluster-values.yaml")

	for _, chartPath := range m.HelmChartPaths {
		charts = append(charts, &HelmChart{
			Path:          chartPath,
			Source:        source.Directory(chartPath),
			ClusterValues: clusterValues,
		})
	}
	return charts
}

// Validate runs helm lint on this chart.
// +check
func (hc *HelmChart) Validate(ctx context.Context) (string, error) {
	if hc.Source == nil {
		return "", fmt.Errorf("HelmChart %s has no source directory; call HelmCharts() first", hc.Path)
	}

	prepared := hc.sourceWithDeps()

	_, err := hc.container(prepared).
		WithExec([]string{"helm", "lint", "/chart"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("helm lint failed for %s: %w", hc.Path, err)
	}

	return fmt.Sprintf("Helm validation passed for %s", hc.Path), nil
}

// Build runs helm template on this chart to verify it renders valid YAML.
// +check
func (hc *HelmChart) Build(ctx context.Context) (string, error) {
	if hc.Source == nil {
		return "", fmt.Errorf("HelmChart %s has no source directory; call HelmCharts() first", hc.Path)
	}

	prepared := hc.sourceWithDeps()
	releaseName := filepath.Base(hc.Path)

	// Parse namespace and release name from application.yaml if present
	namespace := releaseName
	appYaml, err := hc.Source.File("application.yaml").Contents(ctx)
	if err == nil && appYaml != "" {
		for _, line := range strings.Split(appYaml, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "namespace:") {
				ns := strings.TrimSpace(strings.TrimPrefix(trimmed, "namespace:"))
				if ns != "" {
					namespace = ns
				}
			}
			if strings.HasPrefix(trimmed, "releaseName:") {
				rel := strings.TrimSpace(strings.TrimPrefix(trimmed, "releaseName:"))
				if rel != "" {
					releaseName = rel
				}
			}
		}
	}

	args := []string{"helm", "template", releaseName, "/chart", "--namespace", namespace, "--debug"}

	container := hc.container(prepared)

	// Mount cluster values if available
	if hc.ClusterValues != nil {
		container = container.
			WithMountedFile("/cluster-values.yaml", hc.ClusterValues)
		args = append(args, "--values", "/cluster-values.yaml")
	}

	_, err = container.
		WithExec(args).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("helm template failed for %s: %w", hc.Path, err)
	}

	return fmt.Sprintf("Helm template passed for %s", hc.Path), nil
}

// container returns a helm container with the chart mounted and shared caches.
func (hc *HelmChart) container(chartSource *dagger.Directory) *dagger.Container {
	return dag.Container().
		From(helmImage).
		WithMountedDirectory("/chart", chartSource).
		WithWorkdir("/chart").
		WithMountedCache("/root/.cache/helm/repository", dag.CacheVolume("helm-repo-cache")).
		WithMountedCache("/root/.cache/helm/content", dag.CacheVolume("helm-content-cache")).
		WithMountedCache("/root/.config/helm/registry", dag.CacheVolume("helm-registry-cache"))
}

// sourceWithDeps builds chart dependencies, returning the chart directory with
// dependency tarballs populated in charts/ dir.
func (hc *HelmChart) sourceWithDeps() *dagger.Directory {
	return hc.container(hc.Source).
		WithExec([]string{"sh", "-c", `
			if grep -q 'dependencies:' Chart.yaml 2>/dev/null; then
				# Register non-OCI repos
				grep 'repository:' Chart.yaml | awk '{print $2}' | while read -r repo_url; do
					if [ -z "$repo_url" ] || echo "$repo_url" | grep -q '^oci://'; then
						continue
					fi
					repo_name="repo-$(echo "$repo_url" | md5sum | cut -c1-8)"
					helm repo add "$repo_name" "$repo_url" 2>/dev/null || true
				done
				helm repo update 2>/dev/null || true
				helm dependency build --skip-refresh . 2>/dev/null || true
			fi
		`}).
		Directory("/chart")
}

// ValidateHelm runs helm lint on charts with dependency download.
// Each chart is validated with a scoped source directory so that changes
// in one chart don't invalidate the BuildKit cache for other charts.
// When paths are provided, only charts matching the paths are linted.
// +check
func (m *Homelab) ValidateHelm(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	chartPaths := m.HelmChartPaths
	if len(paths) > 0 {
		chartPaths = matchChartPaths(paths, chartPaths)
	}
	if len(chartPaths) == 0 {
		return "Helm validation skipped (no matching charts)", nil
	}

	g := new(errgroup.Group)
	for _, chartPath := range chartPaths {
		hc := &HelmChart{
			Path:   chartPath,
			Source: source.Directory(chartPath),
		}
		g.Go(func() error {
			_, err := hc.Validate(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

	return "Helm validation passed", nil
}

// BuildHelm renders Helm templates for Kubernetes charts to verify they are valid.
// Each chart is rendered with a scoped source directory so that changes
// in one chart don't invalidate the BuildKit cache for other charts.
// When paths are provided, only charts matching the paths are rendered.
// +check
func (m *Homelab) BuildHelm(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	paths []string,
) (string, error) {
	chartPaths := m.HelmChartPaths
	if len(paths) > 0 {
		chartPaths = matchChartPaths(paths, chartPaths)
	}
	if len(chartPaths) == 0 {
		return "Helm template rendering skipped (no matching charts)", nil
	}

	// Check for cluster-values.yaml
	clusterValues := source.File("config/gen/cluster-values.yaml")

	g := new(errgroup.Group)
	for _, chartPath := range chartPaths {
		hc := &HelmChart{
			Path:          chartPath,
			Source:        source.Directory(chartPath),
			ClusterValues: clusterValues,
		}
		g.Go(func() error {
			_, err := hc.Build(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

	return "Helm template rendering passed", nil
}

// helmContainer returns a helm container with shared cache volumes mounted.
// Used by top-level functions that operate on the full source tree.
func (m *Homelab) helmContainer(source *dagger.Directory) *dagger.Container {
	return dag.Container().
		From(helmImage).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithMountedCache("/root/.cache/helm/repository", dag.CacheVolume("helm-repo-cache")).
		WithMountedCache("/root/.cache/helm/content", dag.CacheVolume("helm-content-cache")).
		WithMountedCache("/root/.config/helm/registry", dag.CacheVolume("helm-registry-cache"))
}

// helmSourceWithDeps registers helm repos and builds chart dependencies,
// returning the source directory with dependency tarballs populated in charts/ dirs.
// Used by top-level aggregate functions and tests.
func (m *Homelab) helmSourceWithDeps(source *dagger.Directory, searchPaths string) *dagger.Directory {
	return m.helmContainer(source).
		WithNewFile("/deps.sh", helmDepsScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithEnvVariable("SEARCH_PATHS", searchPaths).
		WithExec([]string{"/deps.sh"}).
		Directory("/src")
}

// matchChartPaths returns chart paths that contain any of the given file paths.
// CUE config changes cause all charts to be returned.
func matchChartPaths(filePaths []string, chartPaths []string) []string {
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
