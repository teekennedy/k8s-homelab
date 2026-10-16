// Tests and lints every Go module in the repo, each in its own scoped
// container so that a change to one module leaves the others cached.

package main

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"
)

const (
	// repoRoot is where a module's slice of the repo is laid out in its
	// container: the module and its local replace targets each at their own
	// repo path beneath it, so a relative replace resolves as in a checkout
	// without ever landing on a system directory.
	repoRoot = "/repo"
	// golangciLintConfigPath is where the repo-root golangci-lint config is
	// mounted, outside of /src, so it's passed explicitly via --config instead of
	// relying on golangci-lint's upward directory search
	golangciLintConfigPath = "/golangci.yaml"
	golangciCache          = "/cache/golangci-lint"
)

// Go runs Go checks over every module in the repo.
type Go struct {
	// +private
	Container *dagger.Container
}

// Modules returns every Go module in the repo, keyed by path. Vendored
// modules are skipped.
func (m *Go) Modules(
	ctx context.Context,
	// +defaultPath="/"
	// The scripts and releases.json are here because Dagger modules go:embed
	// them; without them those modules do not build.
	// +ignore=["*", "!**/*.go", "!**/go.mod", "!**/go.sum", "!.dagger/**/scripts/*.sh", "!.dagger/modules/homelab/releases.json", "!.golangci.yaml"]
	source *dagger.Directory,
) (*GoModules, error) {
	goModFiles, err := source.Glob(ctx, "**/go.mod")
	if err != nil {
		return nil, fmt.Errorf("discovering Go modules: %w", err)
	}
	var paths []string
	for _, f := range goModFiles {
		if strings.Contains(f, "/vendor/") || strings.HasPrefix(f, "vendor/") {
			continue
		}
		paths = append(paths, filepath.Dir(f))
	}
	sort.Strings(paths)
	return &GoModules{Paths: paths, Source: source, Container: m.Container}, nil
}

// GoModules are the repo's Go modules.
// +collection
type GoModules struct {
	// Paths are the modules' directories relative to the repo root.
	// +keys
	Paths []string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
}

// Module returns the Go module at path, carrying only its own files and its
// local replace targets, so that it caches independently of the others.
// +get
func (ms *GoModules) Module(ctx context.Context, path string) (*GoModule, error) {
	return newModule(ctx, ms.Source, path, ms.Container)
}

func newModule(ctx context.Context, source *dagger.Directory, modPath string, container *dagger.Container) (*GoModule, error) {
	gomod, err := source.File(path.Join(modPath, "go.mod")).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading %s/go.mod: %w", modPath, err)
	}
	gm := &GoModule{
		Path:       modPath,
		Source:     source.Directory(modPath),
		ConfigFile: source.File(".golangci.yaml"),
		Container:  container,
	}
	for _, target := range localReplaces(gomod) {
		repoPath := path.Join(modPath, target)
		if strings.HasPrefix(repoPath, "../") {
			return nil, fmt.Errorf("%s/go.mod replaces a module with %s, which is outside the repo", modPath, target)
		}
		gm.Replaces = append(gm.Replaces, &GoReplace{
			Mount:  path.Join(repoRoot, repoPath),
			Source: source.Directory(repoPath),
		})
	}
	return gm, nil
}

// GoModule is one Go module.
type GoModule struct {
	// Path is the module's directory relative to the repo root (e.g. "cmd/lab").
	Path string
	// +private
	Source *dagger.Directory
	// Replaces are the modules its go.mod replaces with a local path, which
	// have to sit at that path relative to Source for it to build.
	// +private
	Replaces []*GoReplace
	// +private
	ConfigFile *dagger.File
	// +private
	Container *dagger.Container
}

// GoReplace is a local `replace` target, mounted where the module expects it.
type GoReplace struct {
	// Mount is the target's absolute path in the module's container.
	Mount string
	// Source is the target's scoped source directory.
	Source *dagger.Directory
}

// workdir is the module's directory in its container.
func (gm *GoModule) workdir() string {
	return path.Join(repoRoot, gm.Path)
}

// withSource mounts the module and its local replace targets into the
// toolchain.
func (gm *GoModule) withSource() *dagger.Container {
	container := gm.Container.WithMountedDirectory(gm.workdir(), gm.Source)
	for _, r := range gm.Replaces {
		container = container.WithMountedDirectory(r.Mount, r.Source)
	}
	return container.WithWorkdir(gm.workdir())
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

// Test runs the module's Go tests.
// +check
func (gm *GoModule) Test(ctx context.Context) error {
	_, err := gm.withSource().
		WithExec([]string{"go", "test", "./..."}).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("go test failed in %s: %w", gm.Path, withExecOutput(err))
	}
	return nil
}

// Lint runs `go mod tidy` followed by `golangci-lint run --fix` on the module,
// returning the resulting changes. It fails if issues remain that --fix cannot
// resolve (e.g. cyclop, gosec).
// +generate
func (gm *GoModule) Lint(ctx context.Context) (*dagger.Changeset, error) {
	fixed, err := gm.withSource().
		WithExec([]string{"go", "mod", "tidy"}).
		WithEnvVariable("GOLANGCI_LINT_CACHE", golangciCache).
		WithMountedCache(golangciCache, dag.CacheVolume("homelab-golangci-lint-/"+gm.Path)).
		WithMountedFile(golangciLintConfigPath, gm.ConfigFile).
		WithExec([]string{"golangci-lint", "run", "--fix", "--config", golangciLintConfigPath, "./..."}).
		Sync(ctx)
	if err != nil {
		// golangci-lint reports the issues it could not fix on stdout, which is
		// the only part of the failure worth reading. Without this the caller
		// gets a bare "exit code: 1" and has to go find the trace.
		return nil, fmt.Errorf("go lint failed in %s: %w", gm.Path, withExecOutput(err))
	}

	return rerooted(gm.Path, gm.Source, fixed.Directory(gm.workdir())), nil
}
