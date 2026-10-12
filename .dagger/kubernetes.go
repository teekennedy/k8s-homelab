package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	"dagger/homelab/internal/dagger"
)

// polarisConfigPath is the repo-wide Polaris config: which checks run, and the
// custom ones.
const polarisConfigPath = "k8s/polaris.yaml"

// Polaris runs Fairwinds Polaris audit on this release's rendered manifests.
// Exits non-zero when any enabled check fails, whatever its severity.
// baseConfig is the contents of polarisConfigPath. If the chart directory
// contains a polaris.yaml, its exemptions are added to it (for expected RBAC or
// privilege requirements).
func (r *helmfileRelease) Polaris(ctx context.Context, toolchain *dagger.Container, baseConfig string) error {
	args := []string{
		"polaris", "audit",
		"--audit-path", "/rendered.yaml",
		"--format", "pretty",
		"--only-show-failed-tests",
		"--set-exit-code-below-score", "100",
		"--config", "/polaris.yaml",
	}

	if _, err := r.polarisContainer(ctx, toolchain, baseConfig).WithExec(args).Sync(ctx); err != nil {
		return fmt.Errorf("polaris failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// polarisContainer returns the render container with this release's Polaris
// config at /polaris.yaml: baseConfig, followed by the chart's polaris.yaml
// when it has one. The two are concatenated, so they must not share a top-level
// key.
func (r *helmfileRelease) polarisContainer(ctx context.Context, toolchain *dagger.Container, baseConfig string) *dagger.Container {
	cfg := baseConfig
	if chartCfg, err := r.Source.File(path.Join(r.Path, "polaris.yaml")).Contents(ctx); err == nil && chartCfg != "" {
		cfg = strings.TrimRight(cfg, "\n") + "\n" + chartCfg
	}

	return r.rendered(toolchain).WithNewFile("/polaris.yaml", cfg)
}

// Kubeconform validates this release's rendered manifests against JSON schemas in strict mode.
// Uses the datreeio CRDs-catalog (baked into the container layer) to validate custom resources
// in addition to built-in Kubernetes schemas. Unknown schemas not in the catalog are skipped.
//
// If the chart directory contains a kubeconform.yaml, its skipKinds list is filtered out of the
// manifest before validation. Use this for kinds whose catalog schema is known to be stale.
func (r *helmfileRelease) Kubeconform(ctx context.Context, toolchain *dagger.Container) error {
	// Parse per-chart skip list
	var skipKinds []string
	if cfg, err := r.Source.File(path.Join(r.Path, "kubeconform.yaml")).Contents(ctx); err == nil && cfg != "" {
		skipKinds = parseKubeconformSkipKinds(cfg)
	}

	args := []string{
		"kubeconform",
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

	// The schemas are layered on the render container so the render itself,
	// which is shared with the other checks, stays a cache hit.
	if _, err := withCRDSchemas(r.rendered(toolchain)).WithExec(args).Sync(ctx); err != nil {
		return fmt.Errorf("kubeconform failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
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

// crdsCatalogURL is the datreeio CRDs-catalog archive used to validate custom resources
// that have no built-in Kubernetes schema.
const crdsCatalogURL = "https://github.com/datreeio/CRDs-catalog/archive/refs/heads/main.tar.gz"

// withCRDSchemas returns the container with the datreeio CRDs-catalog unpacked at /schemas.
func withCRDSchemas(toolchain *dagger.Container) *dagger.Container {
	catalog := dag.HTTP(crdsCatalogURL)

	return toolchain.
		WithMountedFile("/tmp/crds-catalog.tar.gz", catalog).
		WithExec([]string{"mkdir", "-p", "/schemas"}).
		WithExec([]string{"tar", "-xz", "--strip-components=1", "-C", "/schemas", "-f", "/tmp/crds-catalog.tar.gz"})
}

// ValidatePolaris runs Polaris audit across all Helm charts, once per
// environment, on the manifests helmfile renders.
// When paths are provided, only matching charts are validated.
// +check
func (m *Homelab) ValidatePolaris(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// Helmfile environments to render with.
	// +optional
	// +default=["production"]
	environments []string,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	baseConfig, err := source.File(polarisConfigPath).Contents(ctx)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", polarisConfigPath, err)
	}

	n, err := m.forEachHelmfileRelease(ctx, source, environments, paths, container, "polaris validation",
		func(ctx context.Context, r *helmfileRelease, c *dagger.Container) error {
			return r.Polaris(ctx, c, baseConfig)
		})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "Polaris validation skipped (no matching charts)", nil
	}
	return fmt.Sprintf("Polaris validation passed (%d releases)", n), nil
}

// ValidateKubeconform runs kubeconform in strict mode across all Helm charts,
// once per environment, on the manifests helmfile renders.
// When paths are provided, only matching charts are validated.
// +check
func (m *Homelab) ValidateKubeconform(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// Helmfile environments to render with.
	// +optional
	// +default=["production"]
	environments []string,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	n, err := m.forEachHelmfileRelease(ctx, source, environments, paths, container, "kubeconform validation",
		func(ctx context.Context, r *helmfileRelease, c *dagger.Container) error {
			return r.Kubeconform(ctx, c)
		})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "Kubeconform validation skipped (no matching charts)", nil
	}
	return fmt.Sprintf("Kubeconform validation passed (%d releases)", n), nil
}
