package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dagger/homelab/internal/dagger"
)

// discoverTerraformModulePaths finds all Terraform module directories in source.
func discoverTerraformModulePaths(ctx context.Context, source *dagger.Directory) []string {
	tfFiles, _ := source.Glob(ctx, "terraform/**/.terraform.lock.hcl")
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

func terraformModuleName(path string) string {
	if path == "terraform" {
		return "root"
	}
	name, _ := strings.CutPrefix(path, "terraform/")
	return name
}

// terraformContainer returns the given container with terraform environment
// variables and cache dirs. If container is nil, it defaults to ciContainer().
func (m *Homelab) terraformContainer(container *dagger.Container) *dagger.Container {
	const tfPluginCache = "/cache/terraform/plugins"

	if container == nil {
		container = m.ciContainer()
	}

	return container.
		WithEnvVariable("TF_PLUGIN_CACHE_DIR", tfPluginCache).
		WithMountedCache(tfPluginCache, dag.CacheVolume("homelab-tf-plugins"))
}

// initTerraformModule runs tofu init against the module given by modPath and
// returns a Changeset of files modified, as well as the initialized container.
func (m *Homelab) initTerraformModule(ctx context.Context, source *dagger.Directory, container *dagger.Container, modPath string) (*dagger.Changeset, *dagger.Container, error) {
	modWorkdir := "/src/" + modPath

	updated, err := container.
		WithMountedDirectory("/src", source).
		WithWorkdir(modWorkdir).
		WithExec([]string{"echo", ("================ " + terraformModuleName(modPath) + " ================")}).
		WithExec([]string{"tofu", "init", "-backend=false"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, nil, fmt.Errorf("terraform module %s: init failed:\n%s", modPath, execErr.Stderr)
		}
		return nil, nil, fmt.Errorf("terraform module %s: init failed: %w", modPath, err)
	}

	before := dag.Directory().WithDirectory(modPath, source.Directory(modPath))
	after := dag.Directory().WithDirectory(modPath, updated.Directory(modWorkdir)).WithoutDirectory(modPath + "/.terraform")

	return after.Changes(before), updated, nil
}

// InitTerraform runs tofu init for all Terraform/OpenTofu modules.
//
// Returns a changeset. Use `dagger generate init-terraform --auto-apply` to apply
// lockfile changes produced by provider initialization.
// +generate
func (m *Homelab) InitTerraform(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*", "terraform/**/.terraform/**", "terraform/**/*.tfstate", "terraform/**/*.tfstate.*"]
	source *dagger.Directory,
	// +optional
	container *dagger.Container,
) (*dagger.Changeset, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return dag.Changeset(), nil
	}
	container = m.terraformContainer(container)

	changesets := make([]*dagger.Changeset, len(modulePaths))
	errs := make([]error, len(modulePaths))

	var wg sync.WaitGroup
	for i, modPath := range modulePaths {
		wg.Go(func() {
			changesets[i], _, errs[i] = m.initTerraformModule(ctx, source, container, modPath)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return dag.Changeset().WithChangesets(changesets), nil
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
	container = m.terraformContainer(container)

	formatted := container.
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"tofu", "fmt", "-recursive", "terraform"}).
		Directory("/src")

	return formatted.Changes(source)
}

// validateTerraform runs tofu init and tofu validate for this module, in the given
// toolchain container.
func (m *Homelab) validateTerraformModule(ctx context.Context, source *dagger.Directory, container *dagger.Container, modPath string) (*dagger.Changeset, error) {
	if source == nil {
		return nil, fmt.Errorf("terraform module %s: no source directory given", modPath)
	}
	if container == nil {
		return nil, fmt.Errorf("terraform module %s: no toolchain container given", modPath)
	}

	modWorkdir := "/src/" + modPath

	_, initializedContainer, err := m.initTerraformModule(ctx, source, container, modPath)
	if err != nil {
		return nil, err
	}

	fixed, err := initializedContainer.
		WithExec([]string{"tofu", "validate"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("terraform module %s: validation failed:\n%s", modPath, execErr.Stderr)
		}
		return nil, fmt.Errorf("terraform module %s: validation failed: %w", modPath, err)
	}

	before := dag.Directory().WithDirectory(modPath, source.Directory(modPath))
	after := dag.Directory().WithDirectory(modPath, fixed.Directory(modWorkdir)).WithoutDirectory(modPath + "/.terraform")

	return after.Changes(before), nil
}

// ValidateTerraform runs tofu init and tofu validate on all discovered Terraform modules.
// Each module is validated independently for parallel execution and individual
// error reporting.
// When paths are provided, only matching modules are validated.
// +generate
func (m *Homelab) ValidateTerraform(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*", "terraform/**/.terraform/**", "terraform/**/*.tfstate", "terraform/**/*.tfstate.*"]
	source *dagger.Directory,
	// +optional
	container *dagger.Container,
) (*dagger.Changeset, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return dag.Changeset(), nil
	}
	container = m.terraformContainer(container)

	changesets := make([]*dagger.Changeset, len(modulePaths))
	errs := make([]error, len(modulePaths))

	var wg sync.WaitGroup
	for i, modPath := range modulePaths {
		wg.Go(func() {
			changesets[i], errs[i] = m.validateTerraformModule(ctx, source, container, modPath)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return dag.Changeset().WithChangesets(changesets), nil
}
