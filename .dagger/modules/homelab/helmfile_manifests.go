package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	"dagger/homelab/internal/dagger"
)

// Polaris runs Fairwinds Polaris audit on this release's rendered manifests.
// Exits non-zero when any enabled check fails, whatever its severity. The
// checks come from the repo-wide k8s/polaris.yaml; if the chart directory
// contains a polaris.yaml, its exemptions are added to it (for expected RBAC or
// privilege requirements).
// +check
func (r *HelmfileRelease) Polaris(ctx context.Context) error {
	args := []string{
		"polaris", "audit",
		"--audit-path", "/rendered.yaml",
		"--format", "pretty",
		"--only-show-failed-tests",
		"--set-exit-code-below-score", "100",
		"--config", "/polaris.yaml",
	}

	container, err := r.polarisContainer(ctx)
	if err != nil {
		return err
	}
	if _, err := container.WithExec(args).Sync(ctx); err != nil {
		return fmt.Errorf("polaris failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// polarisContainer returns the render container with this release's Polaris
// config at /polaris.yaml: the repo-wide config, followed by the chart's
// polaris.yaml when it has one. The two are concatenated, so they must not
// share a top-level key.
func (r *HelmfileRelease) polarisContainer(ctx context.Context) (*dagger.Container, error) {
	cfg, err := r.PolarisConfig.Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", polarisConfigPath, err)
	}
	if chartCfg, err := r.Source.File(path.Join(r.Path, "polaris.yaml")).Contents(ctx); err == nil && chartCfg != "" {
		cfg = strings.TrimRight(cfg, "\n") + "\n" + chartCfg
	}

	return r.rendered().WithNewFile("/polaris.yaml", cfg), nil
}

// Kubeconform validates this release's rendered manifests against JSON schemas in strict mode.
// Uses the datreeio CRDs-catalog (baked into the container layer) to validate custom resources
// in addition to built-in Kubernetes schemas. Unknown schemas not in the catalog are skipped.
//
// If the chart directory contains a kubeconform.yaml, its skipKinds list is filtered out of the
// manifest before validation. Use this for kinds whose catalog schema is known to be stale.
// +check
func (r *HelmfileRelease) Kubeconform(ctx context.Context) error {
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
	if _, err := withCRDSchemas(r.rendered()).WithExec(args).Sync(ctx); err != nil {
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
