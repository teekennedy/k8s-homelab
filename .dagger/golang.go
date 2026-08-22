package main

import (
	"context"
	"dagger/homelab/internal/dagger"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh", "!.golangci.yaml"]
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

// FormatGo formats Go files using golangci-lint's configured formatters
// (gofumpt) across all discovered modules.
// Returns a changeset. Use `dagger call format-go --auto-apply` to apply.
// +generate
func (m *Homelab) FormatGo(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh", "!.golangci.yaml"]
	source *dagger.Directory,
) *dagger.Changeset {
	formatted := m.goFormat(ctx, source)
	return formatted.Changes(source)
}

// golangciLintConfigPath is where the repo-root golangci-lint config is
// mounted, outside of /src, so it's passed explicitly via --config instead of
// relying on golangci-lint's upward directory search (which stops at a go.mod
// boundary and won't find the repo-root config for nested modules) and
// without polluting the returned /src tree that goFormat/goFix diff against
// the original source to build a Changeset.
const golangciLintConfigPath = "/golangci.yaml"

// goFormat runs `golangci-lint fmt` on all Go modules, returning the formatted directory.
func (m *Homelab) goFormat(ctx context.Context, source *dagger.Directory) *dagger.Directory {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return source
	}

	configFile := source.File(".golangci.yaml")
	fmtArgs := []string{"golangci-lint", "fmt", "--config", golangciLintConfigPath, "./..."}

	container := golangciLintContainer().
		WithMountedFile(golangciLintConfigPath, configFile).
		WithMountedDirectory("/src", source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"})

	for _, modPath := range goModulePaths {
		container = container.
			WithWorkdir("/src/" + modPath).
			WithExec(fmtArgs)
	}

	return container.Directory("/src")
}

// LintGo tidies go.mod/go.sum and applies golangci-lint's autofixes (including
// formatting) across all discovered Go modules.
// Fails if issues remain that --fix cannot resolve (e.g. cyclop, gosec).
// Returns a changeset. Use `dagger call lint-go --auto-apply` to apply.
// +generate
func (m *Homelab) LintGo(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh", "!.golangci.yaml"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	fixed, err := m.goFix(ctx, source)
	if err != nil {
		return nil, err
	}
	return fixed.Changes(source), nil
}

// goFix runs `go mod tidy` followed by `golangci-lint run --fix` on all Go modules,
// returning the fixed directory. Errors from either step (including unfixable
// lint issues that --fix leaves behind) are returned rather than swallowed, so
// LintGo fails when there's more to fix than autofixing can handle.
func (m *Homelab) goFix(ctx context.Context, source *dagger.Directory) (*dagger.Directory, error) {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return source, nil
	}

	configFile := source.File(".golangci.yaml")
	fixArgs := []string{"golangci-lint", "run", "--fix", "--config", golangciLintConfigPath, "./..."}

	container := golangciLintContainer().
		WithMountedFile(golangciLintConfigPath, configFile).
		WithMountedDirectory("/src", source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"})

	for _, modPath := range goModulePaths {
		fixed, err := container.
			WithWorkdir("/src/" + modPath).
			WithExec([]string{"go", "mod", "tidy"}).
			WithExec(fixArgs).
			Sync(ctx)
		if err != nil {
			return nil, fmt.Errorf("go fix failed in %s: %w", modPath, err)
		}
		container = fixed
	}

	return container.Directory("/src"), nil
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
