package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dagger/release/internal/dagger"

	"golang.org/x/sync/errgroup"
)

const (
	// releaseRepository is where release images are published, tagged with
	// their release tag.
	releaseRepository = "ghcr.io/teekennedy/k8s-homelab"
	// nixImageLabel holds a devenv image's Nix store path; the nix module's
	// Devenv.Container sets it.
	nixImageLabel = "io.github.teekennedy.homelab.nix-image"
)

// artifactPlan is whether a new release of an artifact would publish a
// different image than its latest one.
type artifactPlan struct {
	Changed bool   `json:"changed"`
	Reason  string `json:"reason"`
}

// Plan reports which artifacts a release of this checkout would publish a
// different image for, as JSON keyed by artifact prefix, for release.sh.
//
// Only Nix-built artifacts can be unchanged: their image is the store path
// devenv evaluates, without building anything, and the published image
// records its own in a label. Any other artifact, and one whose published
// image has no such label, is changed.
func (m *Release) Plan(
	ctx context.Context,
	// The latest release tag of each artifact to compare, such as
	// devenv-ci-1.2.3. An artifact with none is changed.
	published []string,
) (*dagger.File, error) {
	plans := make([]artifactPlan, len(published))
	prefixes := make([]string, len(published))
	g, gctx := errgroup.WithContext(ctx)
	for i, tag := range published {
		_, artifact, err := artifactForTag(tag)
		if err != nil {
			return nil, err
		}
		prefixes[i] = artifact.Prefix
		g.Go(func() (err error) {
			plans[i], err = m.planArtifact(gctx, artifact, tag)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("planning the release: %w", err)
	}

	result := make(map[string]artifactPlan, len(published))
	for i, prefix := range prefixes {
		result[prefix] = plans[i]
		fmt.Printf("%s: %s\n", prefix, plans[i].Reason)
	}
	contents, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the release plan: %w", err)
	}
	return dag.File("release-plan.json", string(contents)+"\n"), nil
}

// planArtifact compares the image this checkout builds for artifact with the
// one published as tag.
func (m *Release) planArtifact(ctx context.Context, artifact releaseArtifact, tag string) (artifactPlan, error) {
	if len(artifact.Profiles) == 0 {
		return artifactPlan{true, "not built with Nix, so always released"}, nil
	}
	current, err := m.nix().Devenv().ImageOutPath(ctx, dagger.NixDevenvImageOutPathOpts{Profiles: artifact.Profiles})
	if err != nil {
		return artifactPlan{}, fmt.Errorf("evaluating the %s image: %w", artifact.Prefix, err)
	}

	image := dag.Container().From(releaseRepository+":"+tag, dagger.ContainerFromOpts{NoLock: true})
	recorded, err := image.Label(ctx, nixImageLabel)
	if err != nil {
		return artifactPlan{true, fmt.Sprintf("could not read %s: %v", tag, err)}, nil
	}
	platform, err := image.Platform(ctx)
	if err != nil {
		return artifactPlan{true, fmt.Sprintf("could not read the platform of %s: %v", tag, err)}, nil
	}
	engine, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return artifactPlan{}, fmt.Errorf("reading the engine's platform: %w", err)
	}
	return compareImage(tag, current, recorded, platform, engine), nil
}

// compareImage decides whether current, the image path evaluated on engine's
// platform, is what tag published: recorded, built for platform.
func compareImage(tag, current, recorded string, platform, engine dagger.Platform) artifactPlan {
	switch {
	case recorded == "":
		return artifactPlan{true, tag + " records no Nix image"}
	case osArch(platform) != osArch(engine):
		// A store path is only comparable on the platform it was evaluated for.
		return artifactPlan{true, fmt.Sprintf("%s is for %s, not the engine's %s", tag, platform, engine)}
	case recorded != current:
		return artifactPlan{true, fmt.Sprintf("image differs from %s's", tag)}
	default:
		return artifactPlan{false, "image unchanged since " + tag}
	}
}

// osArch is a platform without its variant: an image's config often leaves
// out the "v8" the engine reports for arm64.
func osArch(p dagger.Platform) string {
	parts := strings.Split(string(p), "/")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}
