package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// discoverDevenvProjectPaths finds all devenv project directories in source,
// identified by the presence of a devenv.yaml file.
func discoverDevenvProjectPaths(ctx context.Context, source *dagger.Directory) []string {
	devenvYamlFiles, _ := source.Glob(ctx, "**/devenv.yaml")
	var paths []string
	for _, f := range devenvYamlFiles {
		paths = append(paths, filepath.Dir(f))
	}
	sort.Strings(paths)
	return paths
}

// directoryAt resolves a discovered project path against a base directory.
// filepath.Dir returns "." for a devenv.yaml at the root, and Directory
// doesn't treat "." as an alias for itself, so that case is special-cased.
func directoryAt(base *dagger.Directory, path string) *dagger.Directory {
	if path == "." {
		return base
	}
	return base.Directory(path)
}

// withDirectoryAt is the inverse of directoryAt: it places dir back at path
// within base, special-casing the root project's "." path.
func withDirectoryAt(base *dagger.Directory, path string, dir *dagger.Directory) *dagger.Directory {
	if path == "." {
		return dir
	}
	return base.WithDirectory(path, dir)
}

// DevenvProject is a devenv project with a scoped source directory.
// Each DevenvProject carries only the files for its project, enabling
// per-project caching: changing files in one project won't invalidate
// the cache for other projects.
type DevenvProject struct {
	// Path is the project's directory relative to the repo root (e.g. "." for the root project).
	Path string
	// Source is the project's scoped source directory.
	Source *dagger.Directory
}

// DevenvProjects returns all discovered devenv projects with scoped source directories.
// The +ignore list mirrors DevenvContainer's file selection: devenv.yaml/
// devenv.nix/devenv.lock are what devenv itself reads, plus cmd/lab because
// the root project's devenv.yaml imports it. This doesn't generalize to a
// future devenv project that imports some other path.
func (m *Homelab) DevenvProjects(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/devenv.yaml", "!**/devenv.nix", "!**/devenv.lock", "!cmd/lab/**"]
	source *dagger.Directory,
) []*DevenvProject {
	var projects []*DevenvProject
	for _, projPath := range discoverDevenvProjectPaths(ctx, source) {
		projects = append(projects, &DevenvProject{
			Path:   projPath,
			Source: directoryAt(source, projPath),
		})
	}
	return projects
}

// Update runs `devenv update` for this project, refreshing its devenv.lock
// with the latest commit for every input (nixpkgs, etc.). Returns the
// resulting changeset.
func (p *DevenvProject) Update(ctx context.Context) (*dagger.Changeset, error) {
	if p.Source == nil {
		return nil, fmt.Errorf("DevenvProject %s has no source directory; call DevenvProjects() first", p.Path)
	}

	updated, err := devenvContainer().
		WithMountedDirectory("/src", p.Source).
		WithWorkdir("/src").
		WithExec([]string{"devenv", "update", "--no-tui", "--option", "devenv.warnOnNewVersion:bool", "false"}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("devenv update failed in %s: %w", p.Path, err)
	}

	return updated.Directory("/src").Changes(p.Source), nil
}

// UpdateDevenv runs `devenv update` for every discovered devenv project and
// returns the combined changeset. Each project runs in its own container
// with only its own scoped source mounted, so a lock change in one project
// doesn't invalidate the cache for the others.
//
// Deliberately not tagged +generate. +generate implies a reproducible step
// that recomputes a value from other checked-in inputs (like
// UpdateGoVendorHash). devenv update instead fetches whatever the latest
// commit happens to be for each input right now, so running it twice in a
// row can produce different output — it belongs to CI as a scheduled
// "refresh" step, not as generated code to keep in sync.
// Use `dagger call update-devenv --auto-apply` to apply.
func (m *Homelab) UpdateDevenv(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!**/devenv.yaml", "!**/devenv.nix", "!**/devenv.lock", "!cmd/lab/**"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	projectPaths := discoverDevenvProjectPaths(ctx, source)
	if len(projectPaths) == 0 {
		return source.Changes(source), nil
	}

	projects := make([]*DevenvProject, len(projectPaths))
	for i, projPath := range projectPaths {
		projects[i] = &DevenvProject{
			Path:   projPath,
			Source: directoryAt(source, projPath),
		}
	}

	changesets := make([]*dagger.Changeset, len(projects))
	g := new(errgroup.Group)
	for i, project := range projects {
		g.Go(func() error {
			changeset, err := project.Update(ctx)
			if err != nil {
				return err
			}
			changesets[i] = changeset
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("updating devenv: %w", err)
	}

	result := source
	for i, project := range projects {
		result = withDirectoryAt(result, project.Path, changesets[i].After())
	}

	return result.Changes(source), nil
}
