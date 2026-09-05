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

// initTerraform runs tofu init for all discovered Terraform modules and returns
// the updated source tree. The .terraform working directories are removed so
// the resulting changeset only contains commit-worthy files such as lockfiles.
func (m *Homelab) initTerraform(ctx context.Context, source *dagger.Directory, container *dagger.Container) (*dagger.Directory, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return source, nil
	}
	container = m.terraformContainer(container)

	ctr := container.WithMountedDirectory("/src", source)
	for _, modPath := range modulePaths {
		ctr = ctr.
			WithWorkdir("/src/" + modPath).
			WithExec([]string{"echo", ("================ " + terraformModuleName(modPath) + " ================")}).
			WithExec([]string{"tofu", "init", "-backend=false"}).
			WithoutDirectory("/src/" + modPath + "/.terraform")
	}

	updated, err := ctr.Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("tofu init failed: %w", err)
	}

	return updated.Directory("/src"), nil
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
	initialized, err := m.initTerraform(ctx, source, container)
	if err != nil {
		return nil, err
	}

	return initialized.Changes(source), nil
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

	modSource := source.Directory(modPath)
	modWorkdir := "/src/" + modPath

	fixed, err := container.
		WithMountedDirectory("/src", source).
		WithWorkdir(modWorkdir).
		WithExec([]string{"echo", ("================ " + terraformModuleName(modPath) + " ================")}).
		WithExec([]string{"tofu", "init", "-backend=false"}).
		WithExec([]string{"tofu", "validate"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("terraform module %s: validation failed:\n%s", modPath, execErr.Stderr)
		}
		return nil, fmt.Errorf("terraform module %s: validation failed: %w", modPath, err)
	}

	before := dag.Directory().WithDirectory(modPath, modSource)
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
