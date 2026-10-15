// Formats, lints, locks and validates the OpenTofu modules under terraform/.
package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/terraform/internal/dagger"
)

// Terraform runs OpenTofu checks over terraform/.
type Terraform struct {
	// +private
	Container *dagger.Container
	// +private
	Platforms []string
}

// Formats, lints, locks and validates the OpenTofu modules under terraform/.
func New(
	// Container to run in. It needs tofu and trivy. dagger.toml wires in
	// devenv's toolchain.
	container *dagger.Container,
	// Platforms the lock generator computes provider hashes for.
	// +optional
	// +default=["linux_arm64", "linux_amd64", "darwin_arm64"]
	platforms []string,
) *Terraform {
	return &Terraform{Container: container, Platforms: platforms}
}

// terraformContainer returns the toolchain with terraform environment
// variables and cache dirs.
func terraformContainer(toolchain *dagger.Container) *dagger.Container {
	const tfPluginCache = "/cache/terraform/plugins"

	return toolchain.
		WithEnvVariable("TF_IN_AUTOMATION", "1").
		WithEnvVariable("TF_PLUGIN_CACHE_DIR", tfPluginCache).
		WithMountedCache(tfPluginCache, dag.CacheVolume("homelab-tf-plugins"))
}

// Modules returns every OpenTofu module under terraform/ — every directory
// with a .terraform.lock.hcl — keyed by path.
func (m *Terraform) Modules(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!terraform/**/*", "terraform/**/.terraform/**", "terraform/**/*.tfstate", "terraform/**/*.tfstate.*"]
	source *dagger.Directory,
) (*Modules, error) {
	lockFiles, err := source.Glob(ctx, "terraform/**/.terraform.lock.hcl")
	if err != nil {
		return nil, fmt.Errorf("discovering terraform modules: %w", err)
	}
	paths := make([]string, len(lockFiles))
	for i, f := range lockFiles {
		paths[i] = filepath.Dir(f)
	}
	sort.Strings(paths)
	return &Modules{Paths: paths, Source: source, Container: m.Container, Platforms: m.Platforms}, nil
}

// Modules are the repo's OpenTofu modules.
// +collection
type Modules struct {
	// Paths are the modules' directories relative to the repo root.
	// +keys
	Paths []string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
	// +private
	Platforms []string
}

// Module returns the OpenTofu module at path. It carries all of terraform/,
// because modules reference their siblings by relative path.
// +get
func (ms *Modules) Module(path string) *TerraformModule {
	return &TerraformModule{Path: path, Source: ms.Source, Container: ms.Container, Platforms: ms.Platforms}
}

// TerraformModule is one OpenTofu module.
type TerraformModule struct {
	// Path is the module's directory relative to the repo root.
	Path string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
	// +private
	Platforms []string
}

// name labels the module in logs: its path under terraform/, or "root".
func (tm *TerraformModule) name() string {
	if tm.Path == "terraform" {
		return "root"
	}
	name, _ := strings.CutPrefix(tm.Path, "terraform/")
	return name
}

// initialized runs tofu init in the module.
func (tm *TerraformModule) initialized(ctx context.Context) (*dagger.Container, error) {
	updated, err := terraformContainer(tm.Container).
		WithMountedDirectory("/src", tm.Source).
		WithWorkdir("/src/" + tm.Path).
		WithExec([]string{"echo", "================ " + tm.name() + " ================"}).
		WithExec([]string{"tofu", "init", "-backend=false", "-input=false"}).
		Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("terraform module %s: init failed:\n%s", tm.Path, execErr.Stderr)
		}
		return nil, fmt.Errorf("terraform module %s: init failed: %w", tm.Path, err)
	}
	return updated, nil
}

// Validate runs tofu init and tofu validate on the module.
// +check
func (tm *TerraformModule) Validate(ctx context.Context) error {
	initialized, err := tm.initialized(ctx)
	if err != nil {
		return err
	}
	if _, err := initialized.WithExec([]string{"tofu", "validate"}).Sync(ctx); err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return fmt.Errorf("terraform module %s: validation failed:\n%s", tm.Path, execErr.Stderr)
		}
		return fmt.Errorf("terraform module %s: validation failed: %w", tm.Path, err)
	}
	return nil
}

// Lock runs tofu providers lock for the module.
//
// Unlike `tofu init`, `tofu providers lock` computes provider hashes for the
// given platforms deterministically, regardless of the platform it runs on.
// This keeps .terraform.lock.hcl stable across different machines, which
// `tofu init` cannot guarantee since it only records hashes for the host
// platform it happens to run on.
// +generate
func (tm *TerraformModule) Lock(ctx context.Context) (*dagger.Changeset, error) {
	initialized, err := tm.initialized(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{"tofu", "providers", "lock"}
	for _, platform := range tm.Platforms {
		args = append(args, "-platform="+platform)
	}
	updated, err := initialized.WithExec(args).Sync(ctx)
	if err != nil {
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("terraform module %s: providers lock failed:\n%s", tm.Path, execErr.Stderr)
		}
		return nil, fmt.Errorf("terraform module %s: providers lock failed: %w", tm.Path, err)
	}

	before := dag.Directory().WithDirectory(tm.Path, tm.Source.Directory(tm.Path))
	after := dag.Directory().
		WithDirectory(tm.Path, updated.Directory("/src/"+tm.Path)).
		WithoutDirectory(tm.Path + "/.terraform")
	return after.Changes(before), nil
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
	formatted := terraformContainer(m.Container).
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithExec([]string{"tofu", "fmt", "-recursive", "terraform"}).
		Directory("/src")

	return formatted.Changes(source)
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
) error {
	out, err := m.Container.
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
			return fmt.Errorf("trivy config failed:\n%s%s", execErr.Stdout, execErr.Stderr)
		}
		return fmt.Errorf("trivy config failed: %w\n%s", err, out)
	}

	return nil
}
