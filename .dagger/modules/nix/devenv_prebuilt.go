package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"dagger/nix/internal/dagger"

	"golang.org/x/sync/errgroup"
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

// releaseVersionPattern is a release tag's semver, as releases.json tags them.
var releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// toolchainFiles are the files the ci toolchain is built from: what the
// toolchainSource constructor argument's +ignore lets through.
var toolchainFiles = []string{"devenv.nix", "devenv.yaml", "devenv.lock", "cmd/lab/devenv.nix"}

// ciReleaseJSON is the latest devenv-ci release, as CiRelease records it.
// The release pipeline commits it to main after publishing the image.
//
//go:embed devenv-ci.json
var ciReleaseJSON []byte

// ciRelease records a published devenv-ci image and the toolchain files it
// was built from.
type ciRelease struct {
	Tag string `json:"tag"`
	// Image is the image's address pinned by digest, so pulling it needs no
	// registry lookup once the engine has it.
	Image    string          `json:"image"`
	Platform dagger.Platform `json:"platform"`
	// Files maps each of toolchainFiles to the sha256 of its contents, or ""
	// when the release had no such file.
	Files map[string]string `json:"files"`
}

// CiRelease records the published devenv-ci image for tag and the toolchain
// files it was built from, as .dagger/modules/nix/devenv-ci.json, which
// CiToolchain reads. Run it from a checkout of tag.
func (m *Devenv) CiRelease(ctx context.Context, tag string) (*dagger.File, error) {
	if version, ok := strings.CutPrefix(tag, ciReleasePrefix+"-"); !ok || !releaseVersionPattern.MatchString(version) {
		return nil, fmt.Errorf("not a %s release tag: %q", ciReleasePrefix, tag)
	}
	image := dag.Container().From(releaseRepository+":"+tag, dagger.ContainerFromOpts{NoLock: true})
	ref, err := image.ImageRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", tag, err)
	}
	platform, err := image.Platform(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the platform of %s: %w", ref, err)
	}
	files, err := fileDigests(ctx, m.ToolchainSource, toolchainFiles)
	if err != nil {
		return nil, err
	}
	record, err := json.MarshalIndent(ciRelease{Tag: tag, Image: ref, Platform: platform, Files: files}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the %s record: %w", tag, err)
	}
	return dag.File("devenv-ci.json", string(record)+"\n"), nil
}

// publishedCiShell returns the recorded devenv-ci image if the toolchain
// files are unchanged since its release and it is for the engine's platform.
//
// It returns nil and no error when the image does not apply, which is the
// ordinary reason to build, and an error when a step failed, which is worth a
// warning. Either way the caller builds.
func (m *Devenv) publishedCiShell(ctx context.Context) (*dagger.Container, error) {
	var release ciRelease
	if err := json.Unmarshal(ciReleaseJSON, &release); err != nil {
		return nil, fmt.Errorf("reading devenv-ci.json: %w", err)
	}
	if release.Image == "" {
		return nil, fmt.Errorf("devenv-ci.json records no %s release", ciReleasePrefix)
	}

	changed, err := m.changedSince(ctx, release)
	if err != nil {
		return nil, err
	}
	if len(changed) > 0 {
		infof("building the devenv ci toolchain: %s changed since %s", strings.Join(changed, ", "), release.Tag)
		return nil, nil
	}

	engine, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the engine's platform: %w", err)
	}
	if !samePlatform(release.Platform, engine) {
		infof("building the devenv ci toolchain: %s is for %s, not the engine's %s", release.Tag, release.Platform, engine)
		return nil, nil
	}
	// Recorded in devenv-ci.json, not dagger.lock.
	image := dag.Container().From(release.Image, dagger.ContainerFromOpts{NoLock: true})
	// Pulled here, so that a failed pull falls back to a build rather than
	// failing every check that runs in it.
	if _, err := image.Sync(ctx); err != nil {
		return nil, fmt.Errorf("pulling %s: %w", release.Image, err)
	}
	infof("using %s as the devenv ci toolchain: no toolchain file changed since %s", release.Image, release.Tag)
	return image, nil
}

// changedSince returns the toolchain files whose contents differ from
// release's.
func (m *Devenv) changedSince(ctx context.Context, release ciRelease) ([]string, error) {
	current, err := fileDigests(ctx, m.ToolchainSource, toolchainFiles)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, path := range toolchainFiles {
		if recorded, ok := release.Files[path]; !ok || recorded != current[path] {
			changed = append(changed, path)
		}
	}
	return changed, nil
}

// fileDigests returns the sha256 of each path's contents in dir, or "" for a
// path that is absent.
func fileDigests(ctx context.Context, dir *dagger.Directory, paths []string) (map[string]string, error) {
	digests := make([]string, len(paths))
	g, gctx := errgroup.WithContext(ctx)
	for i, path := range paths {
		g.Go(func() error {
			exists, err := dir.Exists(gctx, path)
			if err != nil {
				return fmt.Errorf("looking for %s: %w", path, err)
			}
			if !exists {
				return nil
			}
			contents, err := dir.File(path).Contents(gctx)
			if err != nil {
				return fmt.Errorf("reading %s: %w", path, err)
			}
			sum := sha256.Sum256([]byte(contents))
			digests[i] = "sha256:" + hex.EncodeToString(sum[:])
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("hashing the toolchain files: %w", err)
	}
	result := make(map[string]string, len(paths))
	for i, path := range paths {
		result[path] = digests[i]
	}
	return result, nil
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
