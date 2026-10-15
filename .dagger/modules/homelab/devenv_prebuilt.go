package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"dagger/homelab/internal/dagger"
)

// The published devenv-ci image standing in for a toolchain build. A release
// builds that image from the same files, with the same profile, as the ci
// toolchain, so while none of those files differs from the release's, pulling
// it gets the toolchain a build would produce, without the Nix build.

const (
	// releaseRepository is where release images are published, tagged with
	// their release tag.
	releaseRepository = "ghcr.io/teekennedy/k8s-homelab"
	// ciReleasePrefix is the devenv-ci release tags' prefix; see releases.json.
	ciReleasePrefix = "devenv-ci"
)

// toolchainFiles are the files the ci toolchain is built from: what the
// toolchainSource constructor argument's +ignore lets through.
var toolchainFiles = []string{"devenv.nix", "devenv.yaml", "devenv.lock", "cmd/lab/devenv.nix"}

// publishedCiShell returns the devenv-ci image of the nearest release tag on
// HEAD, if the toolchain files are unchanged since that tag and the image is
// for the engine's platform.
//
// It returns nil and no error when the image does not apply, which is the
// ordinary reason to build, and an error when a step failed, which is worth a
// warning. Either way the caller builds.
func (m *Devenv) publishedCiShell(ctx context.Context, ws dagger.Workspace) (*dagger.Container, error) {
	head := ws.Git().Head()
	tag, err := nearestReleaseTag(ctx, head, ciReleasePrefix)
	if err != nil {
		return nil, fmt.Errorf("finding the nearest %s release: %w", ciReleasePrefix, err)
	}
	if tag == "" {
		return nil, fmt.Errorf("no %s release tag is an ancestor of HEAD", ciReleasePrefix)
	}

	changed, err := changedFiles(ctx, head.AsRepository().Tag(tag).Tree(), m.ToolchainSource, toolchainFiles)
	if err != nil {
		return nil, fmt.Errorf("comparing the toolchain files with %s: %w", tag, err)
	}
	if len(changed) > 0 {
		infof("building the devenv ci toolchain: %s changed since %s", strings.Join(changed, ", "), tag)
		return nil, nil
	}

	ref := releaseRepository + ":" + tag
	// Not recorded in dagger.lock: every new release would add an entry, and
	// a release tag already names one image.
	image := dag.Container().From(ref, dagger.ContainerFromOpts{NoLock: true})
	platform, err := image.Platform(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}
	engine, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the engine's platform: %w", err)
	}
	if !samePlatform(platform, engine) {
		infof("building the devenv ci toolchain: %s is for %s, not the engine's %s", ref, platform, engine)
		return nil, nil
	}
	// Pulled here, so that a failed pull falls back to a build rather than
	// failing every check that runs in it.
	if _, err := image.Sync(ctx); err != nil {
		return nil, fmt.Errorf("pulling %s: %w", ref, err)
	}
	infof("using %s as the devenv ci toolchain: no toolchain file changed since %s", ref, tag)
	return image, nil
}

// nearestReleaseTag returns the highest-versioned release tag with prefix that
// is head or an ancestor of it, or "" when there is none.
func nearestReleaseTag(ctx context.Context, head *dagger.GitRef, prefix string) (string, error) {
	repo := head.AsRepository()
	names, err := repo.Tags(ctx, dagger.GitRepositoryTagsOpts{Patterns: []string{"refs/tags/" + prefix + "-*"}})
	if err != nil {
		return "", fmt.Errorf("listing tags: %w", err)
	}
	type release struct {
		tag     string
		version [3]int
	}
	var releases []release
	for _, name := range names {
		tag := strings.TrimPrefix(name, "refs/tags/")
		if version, ok := parseReleaseVersion(strings.TrimPrefix(tag, prefix+"-")); ok {
			releases = append(releases, release{tag, version})
		}
	}
	slices.SortFunc(releases, func(a, b release) int {
		return slices.Compare(b.version[:], a.version[:])
	})
	for _, r := range releases {
		contains, err := head.Contains(ctx, repo.Tag(r.tag))
		if err != nil {
			return "", fmt.Errorf("checking whether HEAD contains %s: %w", r.tag, err)
		}
		if contains {
			return r.tag, nil
		}
	}
	return "", nil
}

// parseReleaseVersion parses a release tag's "major.minor.patch" suffix, the
// only form releaseVersionPattern accepts.
func parseReleaseVersion(s string) ([3]int, bool) {
	var version [3]int
	if !releaseVersionPattern.MatchString(s) {
		return version, false
	}
	for i, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return version, false
		}
		version[i] = n
	}
	return version, true
}

// changedFiles returns the paths whose contents differ between two trees,
// counting a file present in only one of them as changed. Comparing contents
// rather than commits catches uncommitted edits too, and ignores a change
// that was later reverted.
func changedFiles(ctx context.Context, before, after *dagger.Directory, paths []string) ([]string, error) {
	var changed []string
	for _, p := range paths {
		a, err := fileDigest(ctx, before, p)
		if err != nil {
			return nil, err
		}
		b, err := fileDigest(ctx, after, p)
		if err != nil {
			return nil, err
		}
		if a != b {
			changed = append(changed, p)
		}
	}
	return changed, nil
}

// fileDigest returns the digest of a file's contents, or "" if it is absent.
func fileDigest(ctx context.Context, dir *dagger.Directory, path string) (string, error) {
	exists, err := dir.Exists(ctx, path)
	if err != nil {
		return "", fmt.Errorf("looking for %s: %w", path, err)
	}
	if !exists {
		return "", nil
	}
	digest, err := dir.File(path).Digest(ctx, dagger.FileDigestOpts{ExcludeMetadata: true})
	if err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}
	return digest, nil
}

// samePlatform compares two platforms by OS and architecture, ignoring the
// variant: an image's config often leaves out the "v8" the engine reports for
// arm64.
func samePlatform(a, b dagger.Platform) bool {
	osArch := func(p dagger.Platform) string {
		parts := strings.Split(string(p), "/")
		if len(parts) > 2 {
			parts = parts[:2]
		}
		return strings.Join(parts, "/")
	}
	return osArch(a) == osArch(b)
}

// infof and warnf write to the calling function's log, which `dagger check`
// shows under it.
func infof(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}
