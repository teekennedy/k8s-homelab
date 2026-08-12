package main

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

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
	// SharedCharts is the optional k8s/charts directory holding library charts
	// that consumers depend on via `repository: file://../../charts/<name>`.
	// It is mounted alongside Source in a composed repo layout so those relative
	// paths resolve; nil is fine for charts with no such dependency.
	// +private
	SharedCharts *dagger.Directory
}

// helmRepoRoot is where the composed repo layout is mounted in helm containers.
// Charts are mounted at their real repo-relative path under it rather than at a
// flat /chart, so a `file://../../charts/...` dependency resolves the same way
// it does for ArgoCD's repo-server (which clones the whole repo).
const helmRepoRoot = "/repo"

// sharedChartsPath is the repo-relative directory holding library charts.
const sharedChartsPath = "k8s/charts"

// chartDir is the absolute path of this chart inside the helm container.
func (hc *HelmChart) chartDir() string {
	return path.Join(helmRepoRoot, hc.Path)
}

// newHelmChart builds a HelmChart with its scoped source plus the shared library
// charts directory, so file:// dependencies resolve.
func newHelmChart(source *dagger.Directory, chartPath string, clusterValues *dagger.File) *HelmChart {
	return &HelmChart{
		Path:          chartPath,
		Source:        source.Directory(chartPath),
		ClusterValues: clusterValues,
		SharedCharts:  source.Directory(sharedChartsPath),
	}
}

// HelmCharts returns all discovered Helm charts with scoped source directories.
// Each chart's Source is a subdirectory of the +defaultPath source, so
// Directory IDs are stable across sessions and cache independently.
func (m *Homelab) HelmCharts(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/**/*", "!config/gen/cluster-values.yaml", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
) []*HelmChart {
	var charts []*HelmChart

	// Check for cluster-values.yaml (used by template rendering)
	clusterValues := source.File("config/gen/cluster-values.yaml")

	for _, chartPath := range discoverHelmChartPaths(ctx, source) {
		charts = append(charts, newHelmChart(source, chartPath, clusterValues))
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
		WithExec([]string{"helm", "lint", hc.chartDir()}).
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

	manifest, err := hc.renderedManifest(ctx)
	if err != nil {
		return "", fmt.Errorf("helm template failed for %s: %w", hc.Path, err)
	}

	if _, err := manifest.Sync(ctx); err != nil {
		return "", fmt.Errorf("helm template failed for %s: %w", hc.Path, err)
	}

	return fmt.Sprintf("Helm template passed for %s", hc.Path), nil
}

// parseApplicationYaml extracts releaseName and namespace from an application.yaml file's content.
// Both default to defaultName when not present in the content.
func parseApplicationYaml(content, defaultName string) (releaseName, namespace string) {
	releaseName = defaultName
	namespace = defaultName
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "namespace:") {
			if ns := strings.TrimSpace(strings.TrimPrefix(trimmed, "namespace:")); ns != "" {
				namespace = ns
			}
		}
		if strings.HasPrefix(trimmed, "releaseName:") {
			if rel := strings.TrimSpace(strings.TrimPrefix(trimmed, "releaseName:")); rel != "" {
				releaseName = rel
			}
		}
	}
	return releaseName, namespace
}

// renderedManifest runs helm template and returns the rendered YAML as a file.
// The manifest is written to /rendered.yaml inside the container, then extracted.
// Build, Polaris, and Kubeconform all call this so the render is cached once per chart.
func (hc *HelmChart) renderedManifest(ctx context.Context) (*dagger.File, error) {
	prepared := hc.sourceWithDeps()
	defaultName := filepath.Base(hc.Path)
	releaseName, namespace := defaultName, defaultName

	if appYaml, err := hc.Source.File("application.yaml").Contents(ctx); err == nil && appYaml != "" {
		releaseName, namespace = parseApplicationYaml(appYaml, defaultName)
	}

	args := []string{"helm", "template", releaseName, hc.chartDir(), "--namespace", namespace, "--include-crds"}

	container := hc.container(prepared)
	if hc.ClusterValues != nil {
		container = container.WithMountedFile("/cluster-values.yaml", hc.ClusterValues)
		args = append(args, "--values", "/cluster-values.yaml")
	}

	// Redirect helm template stdout to a file so validators can consume it
	cmd := strings.Join(args, " ") + " > /rendered.yaml"
	return container.WithExec([]string{"sh", "-c", cmd}).File("/rendered.yaml"), nil
}

// container returns a helm container with the chart mounted and shared caches.
//
// chartSource is mounted at the chart's repo-relative path inside helmRepoRoot,
// with SharedCharts alongside it, so that only the chart's own files (plus the
// library charts) are in the build — per-chart caching is preserved, and a change
// to a library chart correctly invalidates only its consumers.
func (hc *HelmChart) container(chartSource *dagger.Directory) *dagger.Container {
	composed := dag.Directory().WithDirectory(hc.Path, chartSource)
	if hc.SharedCharts != nil && !strings.HasPrefix(hc.Path, sharedChartsPath+"/") {
		composed = composed.WithDirectory(sharedChartsPath, hc.SharedCharts)
	}

	return dag.Container().
		From(helmImage).
		WithMountedDirectory(helmRepoRoot, composed).
		WithWorkdir(hc.chartDir()).
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
				helm repo update
				helm dependency build --skip-refresh .
			fi
		`}).
		Directory(hc.chartDir())
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
	helmChartPaths := discoverHelmChartPaths(ctx, source)
	if len(paths) > 0 {
		helmChartPaths = matchChartPaths(paths, helmChartPaths)
	}
	if len(helmChartPaths) == 0 {
		return "Helm validation skipped (no matching charts)", nil
	}

	g := new(errgroup.Group)
	for _, chartPath := range helmChartPaths {
		hc := newHelmChart(source, chartPath, nil)
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
	helmChartPaths := discoverHelmChartPaths(ctx, source)
	if len(paths) > 0 {
		helmChartPaths = matchChartPaths(paths, helmChartPaths)
	}
	if len(helmChartPaths) == 0 {
		return "Helm template rendering skipped (no matching charts)", nil
	}

	// Check for cluster-values.yaml (File() is lazy, so check existence via Stat)
	var clusterValues *dagger.File
	cv := source.File("config/gen/cluster-values.yaml")
	if _, err := cv.Sync(ctx); err == nil {
		clusterValues = cv
	}

	g := new(errgroup.Group)
	for _, chartPath := range helmChartPaths {
		hc := newHelmChart(source, chartPath, clusterValues)
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
