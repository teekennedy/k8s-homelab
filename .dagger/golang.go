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

// discoverGoModulePaths finds all Go module directories in source,
// excluding vendored modules (which may be excluded by nested .gitignore
// files not honored by +defaultPath).
func discoverGoModulePaths(ctx context.Context, source *dagger.Directory) []string {
	goModFiles, _ := source.Glob(ctx, "**/go.mod")
	var paths []string
	for _, f := range goModFiles {
		if strings.Contains(f, "/vendor/") || strings.HasPrefix(f, "vendor/") {
			continue
		}
		paths = append(paths, filepath.Dir(f))
	}
	sort.Strings(paths)
	return paths
}

// GoModule is a Go module with a scoped source directory.
// Each GoModule carries only the files for its module, enabling
// per-module caching: changing files in one module won't invalidate
// the cache for other modules.
type GoModule struct {
	// Path is the module's directory relative to the repo root (e.g. "cmd/lab").
	Path string
	// Source is the module's scoped source directory.
	// When populated via GoModules(), this is derived from a +defaultPath
	// parent directory, so its ID is stable across sessions.
	Source *dagger.Directory
}

// GoModules returns all discovered Go modules with scoped source directories.
// Each module's Source is a subdirectory of the +defaultPath source, so
// Directory IDs are stable across sessions and cache independently.
func (m *Homelab) GoModules(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
) []*GoModule {
	var modules []*GoModule
	for _, modPath := range discoverGoModulePaths(ctx, source) {
		modules = append(modules, &GoModule{
			Path:   modPath,
			Source: source.Directory(modPath),
		})
	}
	return modules
}

// Test runs Go tests for this module.
// +check
func (gm *GoModule) Test(ctx context.Context) (string, error) {
	if gm.Source == nil {
		return "", fmt.Errorf("GoModule %s has no source directory; call GoModules() first", gm.Path)
	}

	_, err := golangContainer().
		WithMountedDirectory("/src", gm.Source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"}).
		WithWorkdir("/src").
		WithExec([]string{"go", "test", "./..."}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("go test failed in %s: %w", gm.Path, err)
	}
	return fmt.Sprintf("Go tests passed in %s", gm.Path), nil
}

// Lint runs go vet and go fmt for this module.
// +check
func (gm *GoModule) Lint(ctx context.Context) (string, error) {
	if gm.Source == nil {
		return "", fmt.Errorf("GoModule %s has no source directory; call GoModules() first", gm.Path)
	}

	formatted := golangContainer().
		WithMountedDirectory("/src", gm.Source).
		WithWorkdir("/src").
		WithExec([]string{"go", "vet", "./..."}).
		WithExec([]string{"go", "fmt", "./..."}).
		Directory("/src")

	changeset := formatted.Changes(gm.Source)
	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking for go formatting changes in %s: %w", gm.Path, err)
	}

	if !empty {
		modified, _ := changeset.ModifiedPaths(ctx)
		return "", fmt.Errorf("go files need formatting in %s: %s", gm.Path, strings.Join(modified, ", "))
	}

	return fmt.Sprintf("Go lint passed in %s", gm.Path), nil
}

// LintGo runs Go linting (go vet) and formatting (go fmt).
// Each module is linted with a scoped source directory so that changes
// in one module don't invalidate the BuildKit cache for other modules.
// Fails if any files need formatting. Use `dagger call format-go --auto-apply` to fix.
// +check
func (m *Homelab) LintGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
) (string, error) {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return "Go lint skipped (no modules found)", nil
	}

	g := new(errgroup.Group)
	for _, modPath := range goModulePaths {
		gm := &GoModule{
			Path:   modPath,
			Source: source.Directory(modPath),
		}
		g.Go(func() error {
			_, err := gm.Lint(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", err
	}

	return "Go lint passed", nil
}

// FormatGo formats Go files with go fmt across all discovered modules.
// Returns a changeset. Use `dagger call format-go --auto-apply` to apply.
func (m *Homelab) FormatGo(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
) *dagger.Changeset {
	return m.goFormat(ctx, source).Changes(source)
}

// goFormat runs go vet and go fmt on all Go modules, returning the formatted directory.
func (m *Homelab) goFormat(ctx context.Context, source *dagger.Directory) *dagger.Directory {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return source
	}

	container := golangContainer().
		WithMountedDirectory("/src", source)

	for _, modPath := range goModulePaths {
		targets := []string{"./..."}
		workdir := "/src/" + modPath

		container = container.
			WithWorkdir(workdir).
			WithExec(append([]string{"go", "vet"}, targets...)).
			WithExec(append([]string{"go", "fmt"}, targets...))
	}

	return container.Directory("/src")
}

// TestGo runs Go tests for all discovered Go modules.
// Each module is tested with a scoped source directory so that changes
// in one module don't invalidate the BuildKit cache for other modules.
// +check
func (m *Homelab) TestGo(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
) (string, error) {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return "Go tests skipped (no modules found)", nil
	}

	g := new(errgroup.Group)
	for _, modPath := range goModulePaths {
		gm := &GoModule{
			Path:   modPath,
			Source: source.Directory(modPath),
		}
		g.Go(func() error {
			_, err := gm.Test(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("go test failed: %w", err)
	}

	return "Go tests passed", nil
}
