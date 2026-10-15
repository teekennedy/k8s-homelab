// Formats, lints, locks and validates the OpenTofu modules under terraform/.
package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dagger/terraform/internal/dagger"
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

// Terraform runs OpenTofu checks over terraform/.
type Terraform struct {
	// +private
	Container *dagger.Container
}

// Formats, lints, locks and validates the OpenTofu modules under terraform/.
func New(
	// Container to run in. It needs tofu and trivy; defaults to the devenv ci
	// toolchain.
	// +optional
	container *dagger.Container,
) *Terraform {
	return &Terraform{Container: container}
}

func (m *Terraform) toolchain() *dagger.Container {
	if m.Container != nil {
		return m.Container
	}
	return dag.Devenv().Toolchain()
}

// terraformContainer returns the toolchain with terraform environment
// variables and cache dirs.
func (m *Terraform) terraformContainer() *dagger.Container {
	const tfPluginCache = "/cache/terraform/plugins"

	return m.toolchain().
		WithEnvVariable("TF_IN_AUTOMATION", "1").
		WithEnvVariable("TF_PLUGIN_CACHE_DIR", tfPluginCache).
		WithMountedCache(tfPluginCache, dag.CacheVolume("homelab-tf-plugins"))
}

// initTerraformModule runs tofu init against the module given by modPath and
// returns a Changeset of files modified, as well as the initialized container.
func (m *Terraform) initTerraformModule(ctx context.Context, source *dagger.Directory, container *dagger.Container, modPath string) (*dagger.Container, error) {
	modWorkdir := "/src/" + modPath

	updated, err := container.
		WithMountedDirectory("/src", source).
		WithWorkdir(modWorkdir).
		WithExec([]string{"echo", ("================ " + terraformModuleName(modPath) + " ================")}).
		WithExec([]string{"tofu", "init", "-backend=false", "-input=false"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("terraform module %s: init failed:\n%s", modPath, execErr.Stderr)
		}
		return nil, fmt.Errorf("terraform module %s: init failed: %w", modPath, err)
	}

	return updated, nil
}

// lockTerraformModule runs tofu providers lock against the module given by
// modPath for the given platforms, and returns a Changeset of files modified.
func (m *Terraform) lockTerraformModule(ctx context.Context, source *dagger.Directory, container *dagger.Container, modPath string, platforms []string) (*dagger.Changeset, error) {
	// module must be initialized first
	initializedContainer, err := m.initTerraformModule(ctx, source, container, modPath)
	if err != nil {
		return nil, err
	}
	modWorkdir := "/src/" + modPath

	args := []string{"tofu", "providers", "lock"}
	for _, platform := range platforms {
		args = append(args, "-platform="+platform)
	}

	updated, err := initializedContainer.
		WithExec(args).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("terraform module %s: providers lock failed:\n%s", modPath, execErr.Stderr)
		}
		return nil, fmt.Errorf("terraform module %s: providers lock failed: %w", modPath, err)
	}

	before := dag.Directory().WithDirectory(modPath, source.Directory(modPath))
	after := dag.Directory().WithDirectory(modPath, updated.Directory(modWorkdir)).WithoutDirectory(modPath + "/.terraform")

	return after.Changes(before), nil
}

// Lock runs tofu providers lock for all Terraform/OpenTofu modules.
//
// Unlike `tofu init`, `tofu providers lock` computes provider hashes for the
// given platforms deterministically, regardless of the platform it runs on.
// This keeps .terraform.lock.hcl stable across different machines, which
// `tofu init` cannot guarantee since it only records hashes for the host
// platform it happens to run on.
//
// Returns a changeset. Use `dagger generate terraform:lock -y` to apply
// lockfile changes produced by provider locking.
// +generate
func (m *Terraform) Lock(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*", "terraform/**/.terraform/**", "terraform/**/*.tfstate", "terraform/**/*.tfstate.*"]
	source *dagger.Directory,
	// platforms to compute provider hashes for.
	// +optional
	// +default=["linux_arm64", "linux_amd64", "darwin_arm64"]
	platforms []string,
) (*dagger.Changeset, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return dag.Changeset(), nil
	}
	container := m.terraformContainer()

	changesets := make([]*dagger.Changeset, len(modulePaths))
	errs := make([]error, len(modulePaths))

	var wg sync.WaitGroup
	for i, modPath := range modulePaths {
		wg.Go(func() {
			changesets[i], errs[i] = m.lockTerraformModule(ctx, source, container, modPath, platforms)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return dag.Changeset().WithChangesets(changesets), nil
}

// Format formats Terraform/OpenTofu files with `tofu fmt`.
//
// `tofu fmt -recursive` is run once over the whole terraform/ tree rather than
// per module, because unlike validate it needs no per-module init and the
// modules already share a single source directory.
// Returns a changeset. Use `dagger generate terraform:format -y` to apply.
// +generate
func (m *Terraform) Format(
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*.tf", "!terraform/**/*.tfvars"]
	source *dagger.Directory,
) *dagger.Changeset {
	formatted := m.terraformContainer().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"tofu", "fmt", "-recursive", "terraform"}).
		Directory("/src")

	return formatted.Changes(source)
}

// validateTerraform runs tofu init and tofu validate for this module, in the given
// toolchain container.
func (m *Terraform) validateTerraformModule(ctx context.Context, source *dagger.Directory, container *dagger.Container, modPath string) (*dagger.Changeset, error) {
	if source == nil {
		return nil, fmt.Errorf("terraform module %s: no source directory given", modPath)
	}
	if container == nil {
		return nil, fmt.Errorf("terraform module %s: no toolchain container given", modPath)
	}

	modWorkdir := "/src/" + modPath

	initializedContainer, err := m.initTerraformModule(ctx, source, container, modPath)
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

// Lint runs trivy config against the Terraform/OpenTofu tree to catch
// misconfigurations (unencrypted resources, over-permissive IAM, etc.).
//
// Trivy supersedes tfsec (the two tools' checks now live in the same
// aquasecurity org, and tfsec is in maintenance-only mode), so this uses
// trivy rather than tfsec directly. --skip-check-update and --skip-version-check
// keep it network-independent and quiet.
// +check
func (m *Terraform) Lint(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*.tf", "!terraform/**/*.tfvars"]
	source *dagger.Directory,
) (string, error) {
	out, err := m.toolchain().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{
			"trivy", "config",
			"--quiet",
			"--skip-check-update",
			"--skip-version-check",
			"--exit-code", "1",
			"terraform",
		}).
		Stdout(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return "", fmt.Errorf("trivy config failed:\n%s%s", execErr.Stdout, execErr.Stderr)
		}
		return "", fmt.Errorf("trivy config failed: %w\n%s", err, out)
	}

	return "Terraform linting passed", nil
}

// Validate runs tofu init and tofu validate on all discovered Terraform modules.
// Each module is validated independently for parallel execution and individual
// error reporting.
// When paths are provided, only matching modules are validated.
// +check
func (m *Terraform) Validate(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*", "terraform/**/.terraform/**", "terraform/**/*.tfstate", "terraform/**/*.tfstate.*"]
	source *dagger.Directory,
) (string, error) {
	modulePaths := discoverTerraformModulePaths(ctx, source)
	if len(modulePaths) == 0 {
		return "No terraform modules found", nil
	}
	container := m.terraformContainer()

	errs := make([]error, len(modulePaths))

	var wg sync.WaitGroup
	for i, modPath := range modulePaths {
		wg.Go(func() {
			_, errs[i] = m.validateTerraformModule(ctx, source, container, modPath)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return "", err
	}

	return "Terraform validate passed", nil
}
