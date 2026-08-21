package main

import (
	"context"
	"dagger/homelab/internal/dagger"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
)

// Polaris runs Fairwinds Polaris audit on this chart's rendered manifests.
// Exits non-zero when any danger-level checks fail.
// If the chart directory contains a polaris.yaml, it is used as the Polaris config
// (supports per-chart exemptions for expected RBAC or privilege requirements).
// +check
func (hc *HelmChart) Polaris(ctx context.Context) (string, error) {
	if hc.Source == nil {
		return "", fmt.Errorf("HelmChart %s has no source directory; call HelmCharts() first", hc.Path)
	}

	manifest := hc.renderedManifest(ctx)
	container := hc.polarisContainer(ctx, manifest)

	args := []string{
		"polaris", "audit",
		"--audit-path", "/rendered.yaml",
		"--format", "pretty",
		"--only-show-failed-tests", "true",
		"--set-exit-code-on-danger",
		"--config", "/polaris.yaml",
		"--merge-config",
	}

	out, err := container.WithExec(args).Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("polaris failed for %s: %w\n%s", hc.Path, err, out)
	}

	return fmt.Sprintf("Polaris passed for %s", hc.Path), nil
}

// polarisContainer returns a polaris container with the rendered manifest mounted at
// /rendered.yaml and a merged polaris config at /polaris.yaml. The config always disables
// missingNetworkPolicy and linuxHardening, which crash polaris 10.x with a nil pointer
// when pod templates have no labels/annotations (polaris bug in their Go template renderer).
// Per-chart exemptions from polaris.yaml are appended when present.
func (hc *HelmChart) polarisContainer(ctx context.Context, manifest *dagger.File) *dagger.Container {
	cfg := "checks:\n  missingNetworkPolicy: ignore\n  linuxHardening: ignore\n"

	if chartCfg, err := hc.Source.File("polaris.yaml").Contents(ctx); err == nil && chartCfg != "" {
		cfg += chartCfg
	}

	return dag.Container().
		From(polarisImage).
		WithFile("/rendered.yaml", manifest).
		WithNewFile("/polaris.yaml", cfg)
}

// Kubeconform validates this chart's rendered manifests against JSON schemas in strict mode.
// Uses the datreeio CRDs-catalog (baked into the container layer) to validate custom resources
// in addition to built-in Kubernetes schemas. Unknown schemas not in the catalog are skipped.
//
// If the chart directory contains a kubeconform.yaml, its skipKinds list is filtered out of the
// manifest before validation. Use this for kinds whose catalog schema is known to be stale.
// +check
func (hc *HelmChart) Kubeconform(ctx context.Context) (string, error) {
	if hc.Source == nil {
		return "", fmt.Errorf("HelmChart %s has no source directory; call HelmCharts() first", hc.Path)
	}

	manifest := hc.renderedManifest(ctx)

	// Parse per-chart skip list
	var skipKinds []string
	if cfg, err := hc.Source.File("kubeconform.yaml").Contents(ctx); err == nil && cfg != "" {
		skipKinds = parseKubeconformSkipKinds(cfg)
	}

	args := []string{
		"/kubeconform",
		"-strict",
		"-ignore-missing-schemas",
		"-schema-location", "default",
		"-schema-location", "/schemas/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json",
		"-summary",
	}
	if len(skipKinds) > 0 {
		args = append(args, "-skip", strings.Join(skipKinds, ","))
	}
	args = append(args, "/rendered.yaml")

	out, err := kubeconformContainerWithSchemas().
		WithFile("/rendered.yaml", manifest).
		WithExec(args).
		Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("kubeconform failed for %s: %w\n%s", hc.Path, err, out)
	}

	return fmt.Sprintf("Kubeconform passed for %s", hc.Path), nil
}

// parseKubeconformSkipKinds parses the skipKinds list out of a kubeconform.yaml's
// contents, e.g.:
//
//	skipKinds:
//	  - SomeKind # stale schema in the catalog
func parseKubeconformSkipKinds(cfg string) []string {
	var skipKinds []string
	for _, line := range strings.Split(cfg, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		kind := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if idx := strings.Index(kind, "#"); idx >= 0 {
			kind = strings.TrimSpace(kind[:idx])
		}
		if kind != "" {
			skipKinds = append(skipKinds, kind)
		}
	}
	return skipKinds
}

// kubeconformContainerWithSchemas builds a kubeconform container with the datreeio CRDs-catalog
// baked in at /schemas. Alpine is used as the base instead of the scratch kubeconform image so
// that curl is available to fetch the catalog; the kubeconform binary is copied in from the
// official image.
func kubeconformContainerWithSchemas() *dagger.Container {
	schemasDir := dag.Container().
		From(alpineImage).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithExec([]string{"mkdir", "/schemas"}).
		WithExec([]string{
			"sh", "-c",
			"curl -sSfL https://github.com/datreeio/CRDs-catalog/archive/refs/heads/main.tar.gz" +
				" | tar -xz --strip-components=1 -C /schemas",
		}).
		Directory("/schemas")

	kubeconformBin := dag.Container().From(kubeconformImage).File("/kubeconform")

	return dag.Container().
		From(alpineImage).
		WithFile("/kubeconform", kubeconformBin).
		WithDirectory("/schemas", schemasDir)
}

// ValidatePolaris runs Polaris audit across all Helm charts.
// When paths are provided, only matching charts are validated.
// +check
func (m *Homelab) ValidatePolaris(ctx context.Context,
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
		return "Polaris validation skipped (no matching charts)", nil
	}

	var clusterValues *dagger.File
	cv := source.File("config/gen/cluster-values.yaml")
	if _, err := cv.Sync(ctx); err == nil {
		clusterValues = cv
	}

	g := new(errgroup.Group)
	for _, chartPath := range helmChartPaths {
		hc := newHelmChart(source, chartPath, clusterValues)
		g.Go(func() error {
			_, err := hc.Polaris(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("polaris validation failed: %w", err)
	}

	return fmt.Sprintf("Polaris validation passed (%d charts)", len(helmChartPaths)), nil
}

// ValidateKubeconform runs kubeconform in strict mode across all Helm charts.
// When paths are provided, only matching charts are validated.
// +check
func (m *Homelab) ValidateKubeconform(ctx context.Context,
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
		return "Kubeconform validation skipped (no matching charts)", nil
	}

	var clusterValues *dagger.File
	cv := source.File("config/gen/cluster-values.yaml")
	if _, err := cv.Sync(ctx); err == nil {
		clusterValues = cv
	}

	g := new(errgroup.Group)
	for _, chartPath := range helmChartPaths {
		hc := newHelmChart(source, chartPath, clusterValues)
		g.Go(func() error {
			_, err := hc.Kubeconform(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("kubeconform validation failed: %w", err)
	}

	return fmt.Sprintf("Kubeconform validation passed (%d charts)", len(helmChartPaths)), nil
}
