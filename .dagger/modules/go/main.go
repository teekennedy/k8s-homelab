// Tests and lints every Go module in the repo, each in its own scoped
// container so that a change to one module leaves the others cached.
package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"dagger/go/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// srcRoot is where a module is mounted in its container.
const srcRoot = "/src"

// Go runs Go checks over every module in the repo.
type Go struct {
	// +private
	Container *dagger.Container
}

// Tests and lints every Go module in the repo.
func New(
	// Container to run in. It needs go and golangci-lint; defaults to the
	// devenv ci toolchain.
	// +optional
	container *dagger.Container,
) *Go {
	return &Go{Container: container}
}

func (m *Go) toolchain() *dagger.Container {
	if m.Container != nil {
		return m.Container
	}
	return dag.Devenv().Toolchain()
}

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
	// When populated via Modules(), this is derived from a +defaultPath
	// parent directory, so its ID is stable across sessions.
	Source *dagger.Directory
	// Replaces are the modules its go.mod replaces with a local path, which
	// have to sit at that path relative to Source for it to build.
	Replaces []*GoReplace
}

// GoReplace is a local `replace` target, mounted where the module expects it.
type GoReplace struct {
	// Mount is the target's absolute path in the module's container.
	Mount string
	// Source is the target's scoped source directory.
	Source *dagger.Directory
}

// withSource mounts the module and its local replace targets into container.
func (gm *GoModule) withSource(container *dagger.Container) *dagger.Container {
	container = container.WithMountedDirectory(srcRoot, gm.Source)
	for _, r := range gm.Replaces {
		container = container.WithMountedDirectory(r.Mount, r.Source)
	}
	return container.WithWorkdir(srcRoot)
}

// Modules returns all discovered Go modules with scoped source directories.
// Each module's Source is a subdirectory of the +defaultPath source, so
// Directory IDs are stable across sessions and cache independently.
func (m *Go) Modules(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/**/scripts/*.sh", "!.dagger/modules/release/releases.json", "!.golangci.yaml"]
	source *dagger.Directory,
) ([]*GoModule, error) {
	return goModules(ctx, source)
}

func goModules(ctx context.Context, source *dagger.Directory) ([]*GoModule, error) {
	var modules []*GoModule
	for _, modPath := range discoverGoModulePaths(ctx, source) {
		gomod, err := source.File(path.Join(modPath, "go.mod")).Contents(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading %s/go.mod: %w", modPath, err)
		}
		gm := &GoModule{Path: modPath, Source: source.Directory(modPath)}
		for _, target := range localReplaces(gomod) {
			repoPath := path.Join(modPath, target)
			if strings.HasPrefix(repoPath, "../") {
				return nil, fmt.Errorf("%s/go.mod replaces a module with %s, which is outside the repo", modPath, target)
			}
			gm.Replaces = append(gm.Replaces, &GoReplace{
				Mount:  path.Join(srcRoot, target),
				Source: source.Directory(repoPath),
			})
		}
		modules = append(modules, gm)
	}
	return modules, nil
}

// localReplaces returns the filesystem paths a go.mod's replace directives
// point at, in either the single-line or the block form.
func localReplaces(gomod string) []string {
	var targets []string
	inBlock := false
	for line := range strings.SplitSeq(gomod, "\n") {
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		var directive string
		switch {
		case line == "replace (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			directive = line
		case strings.HasPrefix(line, "replace "):
			directive = strings.TrimPrefix(line, "replace ")
		}
		if target, ok := localReplaceTarget(directive); ok {
			targets = append(targets, target)
		}
	}
	return targets
}

// localReplaceTarget returns the target of one replace directive
// ("old [version] => new [version]") when it is a filesystem path.
func localReplaceTarget(directive string) (string, bool) {
	_, target, ok := strings.Cut(directive, "=>")
	if !ok {
		return "", false
	}
	fields := strings.Fields(target)
	if len(fields) == 0 {
		return "", false
	}
	if strings.HasPrefix(fields[0], "./") || strings.HasPrefix(fields[0], "../") {
		return fields[0], true
	}
	return "", false
}

// Test runs Go tests for this module.
func (gm *GoModule) Test(
	ctx context.Context,
	container *dagger.Container,
) (string, error) {
	if gm.Source == nil {
		return "", fmt.Errorf("GoModule %s has no source directory; call Modules() first", gm.Path)
	}
	if container == nil {
		return "", fmt.Errorf("GoModule Test function called with nil container")
	}

	_, err := gm.withSource(container).
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

// Lint tidies go.mod/go.sum and applies golangci-lint's autofixes (including
// formatting) across all discovered Go modules.
// Fails if issues remain that --fix cannot resolve (e.g. cyclop, gosec).
// Returns a changeset. Use `dagger generate go:lint -y` to apply.
// +generate
func (m *Go) Lint(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/**/scripts/*.sh", "!.dagger/modules/release/releases.json", "!.golangci.yaml"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	goModules, err := goModules(ctx, source)
	if err != nil {
		return nil, err
	}
	if len(goModules) == 0 {
		return dag.Changeset(), nil
	}
	container := m.toolchain()
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
// Go.Lint fails when there's more to fix than autofixing can handle.
func (gm *GoModule) Lint(ctx context.Context, configFile *dagger.File, container *dagger.Container) (*dagger.Changeset, error) {
	if gm.Source == nil {
		return nil, fmt.Errorf("GoModule %s has no source directory; call Modules() first", gm.Path)
	}
	if container == nil {
		return nil, fmt.Errorf("GoModule Lint function called with nil container")
	}

	fixed, err := gm.withSource(container).
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
	// to the module ("main.go"), and `--auto-apply` — which writes relative to
	// the repo root — would drop the file in the wrong place. Grafting each side
	// onto an empty tree at gm.Path restores the repo-relative paths without
	// widening what the exec above had mounted.
	before := dag.Directory().WithDirectory(gm.Path, gm.Source)
	after := dag.Directory().WithDirectory(gm.Path, fixed.Directory(srcRoot))

	return after.Changes(before), nil
}

// Test runs Go tests for all discovered Go modules.
// Each module is tested with a scoped source directory so that changes
// in one module don't invalidate the BuildKit cache for other modules.
// +check
func (m *Go) Test(
	ctx context.Context,
	// +defaultPath="/"
	// The scripts and releases.json are here because Dagger modules go:embed
	// them; without them those modules do not build and `go test` fails at
	// setup.
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/**/scripts/*.sh", "!.dagger/modules/release/releases.json"]
	source *dagger.Directory,
) (string, error) {
	goModules, err := goModules(ctx, source)
	if err != nil {
		return "", err
	}
	if len(goModules) == 0 {
		return "Go tests skipped (no modules found)", nil
	}

	container := m.toolchain()
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
