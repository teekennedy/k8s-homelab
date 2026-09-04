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
		name, _ := strings.CutPrefix(modPath, "terraform/")
		modules = append(modules, &TerraformModule{
			Path:   modPath,
			Name:   name,
			Source: tfDir,
		})
	}
	return modules
}

// Validate runs tofu init and tofu validate for this module, in the given
// toolchain container.
func (tm *TerraformModule) Validate(ctx context.Context, container *dagger.Container) (string, error) {
	if tm.Source == nil {
		return "", fmt.Errorf("TerraformModule %s has no source directory; call TerraformModules() first", tm.Path)
	}
	if container == nil {
		return "", fmt.Errorf("TerraformModule %s: no toolchain container given", tm.Path)
	}

	_, err := container.
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

// FormatTerraform formats Terraform/OpenTofu files with `tofu fmt`.
//
// `tofu fmt -recursive` is run once over the whole terraform/ tree rather than
// per module, because unlike validate it needs no per-module init and the
// modules already share a single source directory.
// Returns a changeset. Use `dagger generate format-terraform --auto-apply` to apply.
// +generate
func (m *Homelab) FormatTerraform(
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*.tf", "!terraform/**/*.tfvars"]
	source *dagger.Directory,
	// +optional
	container *dagger.Container,
) *dagger.Changeset {
	if container == nil {
		container = m.ciContainer()
	}

	formatted := container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"tofu", "fmt", "-recursive", "terraform"}).
		Directory("/src")

	return formatted.Changes(source)
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
	// +optional
	container *dagger.Container,
) (string, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return "Terraform validation skipped (no matching modules)", nil
	}
	if container == nil {
		container = m.ciContainer()
	}

	tfDir := source.Directory("terraform")

	g := new(errgroup.Group)
	for _, modPath := range modulePaths {
		name, _ := strings.CutPrefix(modPath, "terraform/")
		tm := &TerraformModule{
			Path:   modPath,
			Name:   name,
			Source: tfDir,
		}
		g.Go(func() error {
			_, err := tm.Validate(ctx, container)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("terraform validation failed: %w", err)
	}

	return "Terraform validation passed", nil
}
