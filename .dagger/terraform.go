package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// discoverTerraformModulePaths finds all Terraform module directories in source.
func discoverTerraformModulePaths(ctx context.Context, source *dagger.Directory) []string {
	tfFiles, _ := source.Glob(ctx, "terraform/*/*.tf")
	seen := map[string]bool{}
	var paths []string
	for _, f := range tfFiles {
		dir := filepath.Dir(f)
		if !seen[dir] {
			seen[dir] = true
			paths = append(paths, dir)
		}
	}
	sort.Strings(paths)
	return paths
}

// TerraformModule is a Terraform/OpenTofu module with a scoped source directory.
// Unlike Go/Python/Helm modules, Terraform modules can reference siblings via
// relative paths (e.g. "../k8s-secret"), so each module's Source is the full
// terraform/ directory rather than just the module's own files. Per-module
// caching still benefits from parallel execution and individual error reporting.
type TerraformModule struct {
	// Path is the module's directory relative to the repo root
	// (e.g. "terraform/cloudflare").
	Path string
	// Name is the module's directory name (e.g. "cloudflare").
	Name string
	// Source is the full terraform/ directory. Terraform modules can reference
	// siblings via relative paths, so we cannot scope to a single module.
	Source *dagger.Directory
}

// TerraformModules returns all discovered Terraform modules.
// Each module shares the full terraform/ directory as its Source since
// modules can reference siblings. The Path field identifies which module
// to validate.
func (m *Homelab) TerraformModules(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*"]
	source *dagger.Directory,
) []*TerraformModule {
	tfDir := source.Directory("terraform")
	var modules []*TerraformModule
	for _, modPath := range discoverTerraformModulePaths(ctx, source) {
		name := modPath
		if strings.HasPrefix(name, "terraform/") {
			name = strings.TrimPrefix(name, "terraform/")
		}
		modules = append(modules, &TerraformModule{
			Path:   modPath,
			Name:   name,
			Source: tfDir,
		})
	}
	return modules
}

// Validate runs tofu init and tofu validate for this module.
// +check
func (tm *TerraformModule) Validate(ctx context.Context) (string, error) {
	if tm.Source == nil {
		return "", fmt.Errorf("TerraformModule %s has no source directory; call TerraformModules() first", tm.Path)
	}

	_, err := dag.Container().
		From(opentofuImage).
		WithMountedDirectory("/src", tm.Source).
		WithWorkdir("/src/" + tm.Name).
		WithExec([]string{"echo", ("================ " + tm.Name + " ================")}).
		WithExec([]string{"tofu", "init", "-backend=false"}).
		WithExec([]string{"tofu", "validate"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("tofu validate failed in %s: %w", tm.Path, err)
	}

	return fmt.Sprintf("Terraform validation passed in %s", tm.Path), nil
}

// ValidateTerraform runs tofu validate on all discovered Terraform modules.
// Each module is validated independently for parallel execution and individual
// error reporting.
// When paths are provided, only matching modules are validated.
// +check
func (m *Homelab) ValidateTerraform(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*"]
	source *dagger.Directory,
) (string, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return "Terraform validation skipped (no matching modules)", nil
	}

	tfDir := source.Directory("terraform")

	g := new(errgroup.Group)
	for _, modPath := range modulePaths {
		name := modPath
		if strings.HasPrefix(name, "terraform/") {
			name = strings.TrimPrefix(name, "terraform/")
		}
		tm := &TerraformModule{
			Path:   modPath,
			Name:   name,
			Source: tfDir,
		}
		g.Go(func() error {
			_, err := tm.Validate(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

	return "Terraform validation passed", nil
}
