package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

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
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh", ".dagger/dagger.gen.go", ".dagger/internal/**"]
	source *dagger.Directory,
) []*GoModule {
	var modules []*GoModule
	for _, modPath := range m.GoModulePaths {
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

	testPkg := "./..."
	// .dagger module requires generated SDK that is excluded from source
	// to keep directory contents stable for caching; test only pure Go packages
	if gm.Path == ".dagger" {
		testPkg = "./pathutil/..."
	}

	_, err := golangContainer().
		WithMountedDirectory("/src", gm.Source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"}).
		WithWorkdir("/src").
		WithExec([]string{"go", "test", testPkg}).
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

	// .dagger module's root package imports generated SDK which is excluded
	// from source to keep directory contents stable; only lint pure Go packages
	vetPkg := "./..."
	if gm.Path == ".dagger" {
		vetPkg = "./pathutil/..."
	}

	formatted := golangContainer().
		WithMountedDirectory("/src", gm.Source).
		WithWorkdir("/src").
		WithExec([]string{"go", "vet", vetPkg}).
		WithExec([]string{"go", "fmt", vetPkg}).
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
	if len(m.GoModulePaths) == 0 {
		return "Go lint skipped (no modules found)", nil
	}

	g := new(errgroup.Group)
	for _, modPath := range m.GoModulePaths {
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
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
) *dagger.Changeset {
	return m.goFormat(source).Changes(source)
}

// goFormat runs go vet and go fmt on all Go modules, returning the formatted directory.
func (m *Homelab) goFormat(source *dagger.Directory) *dagger.Directory {
	if len(m.GoModulePaths) == 0 {
		return source
	}

	container := golangContainer().
		WithMountedDirectory("/src", source)

	for _, modPath := range m.GoModulePaths {
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
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh", ".dagger/dagger.gen.go", ".dagger/internal/**"]
	source *dagger.Directory,
) (string, error) {
	if len(m.GoModulePaths) == 0 {
		return "Go tests skipped (no modules found)", nil
	}

	g := new(errgroup.Group)
	for _, modPath := range m.GoModulePaths {
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
