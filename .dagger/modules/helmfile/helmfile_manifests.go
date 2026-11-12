package main

import (
	"context"
	"fmt"
	"path"
	"strings"

	"dagger/helmfile/internal/dagger"
)

// Validates the rendered manifests against their schemas.
//
// kubeconform runs in strict mode, with the datreeio CRDs-catalog (baked into
// the container layer) for custom resources alongside the built-in Kubernetes
// schemas. Kinds not in the catalog are skipped.
//
// If the chart directory contains a kubeconform.yaml, its skipKinds list is
// filtered out of the manifest before validation. Use this for kinds whose
// catalog schema is known to be stale.
// +check
func (r *Release) Validate(ctx context.Context) error {
	// The per-chart skip list is optional. Exists is checked first because
	// File on a missing path logs a failed span even when the error is handled.
	var skipKinds []string
	cfgPath := path.Join(r.Path, "kubeconform.yaml")
	exists, err := r.Source.Exists(ctx, cfgPath, dagger.DirectoryExistsOpts{ExpectedType: dagger.ExistsTypeRegularType})
	if err != nil {
		return fmt.Errorf("looking for %s: %w", cfgPath, err)
	}
	if exists {
		cfg, err := r.Source.File(cfgPath).Contents(ctx)
		if err != nil {
			return fmt.Errorf("reading %s: %w", cfgPath, err)
		}
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
