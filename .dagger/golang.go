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
	// ConfigFile is the repo's root .golangci.yaml, used by Lint.
	// +private
	ConfigFile *dagger.File
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
	configFile := source.File(".golangci.yaml")
	var modules []*GoModule
	for _, modPath := range discoverGoModulePaths(ctx, source) {
		modules = append(modules, &GoModule{
			Path:       modPath,
			Source:     source.Directory(modPath),
			ConfigFile: configFile,
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

// Lint runs golangci-lint (linting and formatting checks) and verifies
// go.mod/go.sum are tidy for this module.
// +check
func (gm *GoModule) Lint(ctx context.Context) (string, error) {
	if gm.Source == nil {
		return "", fmt.Errorf("GoModule %s has no source directory; call GoModules() first", gm.Path)
	}

	lintArgs := []string{"golangci-lint", "run", "./..."}
	lintContainer, err := golangciLintContainer(ctx, gm.ConfigFile, lintArgs)
	if err != nil {
		return "", fmt.Errorf("building golangci-lint container for %s: %w", gm.Path, err)
	}

	_, err = lintContainer.
		WithMountedDirectory("/src", gm.Source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"}).
		WithMountedFile("/src/.golangci.yaml", gm.ConfigFile, dagger.ContainerWithMountedFileOpts{Owner: "1000:1000"}).
		WithWorkdir("/src").
		WithExec(lintArgs).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("golangci-lint failed in %s: %w", gm.Path, err)
	}

	_, err = golangContainer().
		WithMountedDirectory("/src", gm.Source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"}).
		WithWorkdir("/src").
		WithExec([]string{"go", "mod", "tidy", "-diff"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("go.mod/go.sum not tidy in %s (run `dagger call fix-go --auto-apply`): %w", gm.Path, err)
	}

	return fmt.Sprintf("Go lint passed in %s", gm.Path), nil
}

// LintGo runs golangci-lint (linting and formatting checks) and verifies
// go.mod/go.sum are tidy across all discovered Go modules.
// Each module is linted with a scoped source directory so that changes
// in one module don't invalidate the BuildKit cache for other modules.
// Use `dagger call fix-go --auto-apply` to fix.
// +check
func (m *Homelab) LintGo(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh", "!.golangci.yaml"]
	source *dagger.Directory,
) (string, error) {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return "Go lint skipped (no modules found)", nil
	}

	configFile := source.File(".golangci.yaml")
	g := new(errgroup.Group)
	for _, modPath := range goModulePaths {
		gm := &GoModule{
			Path:       modPath,
			Source:     source.Directory(modPath),
			ConfigFile: configFile,
		}
		g.Go(func() error {
			_, err := gm.Lint(ctx)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("go lint failed: %w", err)
	}

	return "Go lint passed", nil
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
) (*dagger.Changeset, error) {
	formatted, err := m.goFormat(ctx, source)
	if err != nil {
		return nil, err
	}
	return formatted.Changes(source), nil
}

// golangciLintConfigPath is where the repo-root golangci-lint config is
// mounted, outside of /src, so it's passed explicitly via --config instead of
// relying on golangci-lint's upward directory search (which stops at a go.mod
// boundary and won't find the repo-root config for nested modules) and
// without polluting the returned /src tree that goFormat/goFix diff against
// the original source to build a Changeset.
const golangciLintConfigPath = "/golangci.yaml"

// goFormat runs `golangci-lint fmt` on all Go modules, returning the formatted directory.
func (m *Homelab) goFormat(ctx context.Context, source *dagger.Directory) (*dagger.Directory, error) {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return source, nil
	}

	configFile := source.File(".golangci.yaml")
	fmtArgs := []string{"golangci-lint", "fmt", "--config", golangciLintConfigPath, "./..."}
	base, err := golangciLintContainer(ctx, configFile, fmtArgs)
	if err != nil {
		return nil, err
	}

	container := base.
		WithMountedFile(golangciLintConfigPath, configFile).
		WithMountedDirectory("/src", source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"})

	for _, modPath := range goModulePaths {
		container = container.
			WithWorkdir("/src/" + modPath).
			WithExec(fmtArgs)
	}

	return container.Directory("/src"), nil
}

// FixGo tidies go.mod/go.sum and applies golangci-lint's autofixes (including
// formatting) across all discovered Go modules.
// Returns a changeset. Use `dagger call fix-go --auto-apply` to apply.
// +generate
func (m *Homelab) FixGo(
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
// returning the fixed directory.
func (m *Homelab) goFix(ctx context.Context, source *dagger.Directory) (*dagger.Directory, error) {
	goModulePaths := discoverGoModulePaths(ctx, source)
	if len(goModulePaths) == 0 {
		return source, nil
	}

	configFile := source.File(".golangci.yaml")
	fixArgs := []string{"golangci-lint", "run", "--fix", "--config", golangciLintConfigPath, "./..."}
	base, err := golangciLintContainer(ctx, configFile, fixArgs)
	if err != nil {
		return nil, err
	}

	container := base.
		WithMountedFile(golangciLintConfigPath, configFile).
		WithMountedDirectory("/src", source, dagger.ContainerWithMountedDirectoryOpts{Owner: "1000:1000"})

	for _, modPath := range goModulePaths {
		container = container.
			WithWorkdir("/src/"+modPath).
			WithExec([]string{"go", "mod", "tidy"}).
			// --fix applies every fix it can, but still exits non-zero if issues
			// remain that it can't fix (e.g. cyclop, gosec). That's fine here:
			// Lint is what gates on remaining issues, FixGo just applies what it can.
			WithExec(fixArgs, dagger.ContainerWithExecOpts{Expect: dagger.ReturnTypeAny})
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
