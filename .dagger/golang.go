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
func (gm *GoModule) Test(
	ctx context.Context,
	container *dagger.Container,
) (string, error) {
	if gm.Source == nil {
		return "", fmt.Errorf("GoModule %s has no source directory; call GoModules() first", gm.Path)
	}
	if container == nil {
		return "", fmt.Errorf("GoModule Test function called with nil container")
	}

	_, err := container.
		WithMountedDirectory("/src", gm.Source).
		WithWorkdir("/src").
		WithExec([]string{"go", "test", "./..."}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("go test failed in %s: %w", gm.Path, err)
	}
	return fmt.Sprintf("Go tests passed in %s", gm.Path), nil
}

const (
	// golangciLintConfigPath is where the repo-root golangci-lint config is
	// mounted, outside of /src, so it's passed explicitly via --config instead of
	// relying on golangci-lint's upward directory search
	golangciLintConfigPath = "/golangci.yaml"
	golangciCache          = "/cache/golangci-lint"
)

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
	// +optional
	container *dagger.Container,
) (*dagger.Changeset, error) {
	goModules := m.GoModules(ctx, source)
	if len(goModules) == 0 {
		return dag.Changeset(), nil
	}
	if container == nil {
		container = m.ciContainer()
	}
	configFile := source.File(".golangci.yaml")

	changesets := make([]*dagger.Changeset, len(goModules))
	errs := make([]error, len(goModules))

	var wg sync.WaitGroup
	for i, gm := range goModules {
		wg.Go(func() {
			changesets[i], errs[i] = gm.Lint(ctx, configFile, container)
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	return dag.Changeset().WithChangesets(changesets), nil
}

// Lint runs `go mod tidy` followed by `golangci-lint run --fix` on this module,
// returning the resulting changes. Errors from either step (including unfixable
// lint issues that --fix leaves behind) are returned rather than swallowed, so
// LintGo fails when there's more to fix than autofixing can handle.
func (gm *GoModule) Lint(ctx context.Context, configFile *dagger.File, container *dagger.Container) (*dagger.Changeset, error) {
	if gm.Source == nil {
		return nil, fmt.Errorf("GoModule %s has no source directory; call GoModules() first", gm.Path)
	}
	if container == nil {
		return nil, fmt.Errorf("GoModule Lint function called with nil container")
	}

	fixed, err := container.
		WithMountedDirectory("/src", gm.Source).
		// Set here rather than inherited from ciContainer, so that Lint behaves
		// the same when called with a container of the caller's choosing — which
		// is how the cache tests drive it.
		WithWorkdir("/src").
		WithExec([]string{"go", "mod", "tidy"}).
		WithEnvVariable("GOLANGCI_LINT_CACHE", golangciCache).
		WithMountedCache(golangciCache, dag.CacheVolume("homelab-golangci-lint-/"+gm.Path)).
		WithMountedFile(golangciLintConfigPath, configFile).
		WithExec([]string{"golangci-lint", "run", "--fix", "--config", golangciLintConfigPath, "./..."}).
		Sync(ctx)
	if err != nil {
		// golangci-lint reports the issues it could not fix on stdout, which is
		// the only part of the failure worth reading. Without this the caller
		// gets a bare "exit code: 1" and has to go find the trace.
		if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
			return nil, fmt.Errorf("go lint failed in %s:\n%s%s", gm.Path, execErr.Stdout, execErr.Stderr)
		}
		return nil, fmt.Errorf("go lint failed in %s: %w", gm.Path, err)
	}

	// Re-rooted under gm.Path before diffing. Both sides of a Changeset are
	// module-scoped directories, so diffing them directly yields paths relative
	// to the module ("golang.go"), and `--auto-apply` — which writes relative to
	// the repo root — would drop the file in the wrong place. Grafting each side
	// onto an empty tree at gm.Path restores the repo-relative paths without
	// widening what the exec above had mounted.
	before := dag.Directory().WithDirectory(gm.Path, gm.Source)
	after := dag.Directory().WithDirectory(gm.Path, fixed.Directory("/src"))

	return after.Changes(before), nil
}

// TestGo runs Go tests for all discovered Go modules.
// Each module is tested with a scoped source directory so that changes
// in one module don't invalidate the BuildKit cache for other modules.
// +check
func (m *Homelab) TestGo(
	ctx context.Context,
	// +defaultPath="/"
	// scripts/*.sh is here because .dagger/gomodhash.go go:embeds it; without
	// it that module does not build and `go test` fails at setup.
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/scripts/*.sh"]
	source *dagger.Directory,
	// +optional
	container *dagger.Container,
) (string, error) {
	goModules := m.GoModules(ctx, source)
	if len(goModules) == 0 {
		return "Go tests skipped (no modules found)", nil
	}

	if container == nil {
		container = m.ciContainer()
	}
	g := new(errgroup.Group)
	for _, gm := range goModules {
		g.Go(func() error {
			_, err := gm.Test(ctx, container)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("go test failed: %w", err)
	}

	return "Go tests passed", nil
}
