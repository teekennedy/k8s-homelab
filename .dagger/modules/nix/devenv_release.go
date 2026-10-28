package main

import (
	"context"
	"encoding/json"
	"fmt"

	"dagger/nix/internal/dagger"
)

// nixImageLabel is the image label holding the store path ImageOutPath
// evaluates, which a later release compares its own against.
const nixImageLabel = "io.github.teekennedy.homelab.nix-image"

// releaseChange is whether a release would publish a different image than
// an earlier one.
type releaseChange struct {
	Changed bool   `json:"changed"`
	Reason  string `json:"reason"`
}

// ReleaseChange reports whether the image Container builds for profiles
// differs from the one published as tag, as JSON {"changed", "reason"} for
// scripts/release-plan.sh. It compares the published image's
// io.github.teekennedy.homelab.nix-image label with ImageOutPath, so nothing
// is built; an image without the label has changed.
func (m *Devenv) ReleaseChange(
	ctx context.Context,
	// A published release tag, such as devenv-ci-1.2.3.
	tag string,
	// The devenv profiles that release was built with.
	profiles []string,
) (string, error) {
	change, err := m.releaseChange(ctx, tag, profiles)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(change)
	if err != nil {
		return "", fmt.Errorf("encoding the %s comparison: %w", tag, err)
	}
	return string(out), nil
}

func (m *Devenv) releaseChange(ctx context.Context, tag string, profiles []string) (releaseChange, error) {
	// The published image first: until it has the label, there is nothing to
	// evaluate for.
	image := dag.Container().From(releaseRepository+":"+tag, dagger.ContainerFromOpts{NoLock: true})
	recorded, err := image.Label(ctx, nixImageLabel)
	if err != nil {
		return releaseChange{true, fmt.Sprintf("could not read %s: %v", tag, err)}, nil
	}
	if recorded == "" {
		return releaseChange{true, tag + " records no Nix image"}, nil
	}
	platform, err := image.Platform(ctx)
	if err != nil {
		return releaseChange{true, fmt.Sprintf("could not read the platform of %s: %v", tag, err)}, nil
	}
	engine, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return releaseChange{}, fmt.Errorf("reading the engine's platform: %w", err)
	}
	if !samePlatform(platform, engine) {
		// A store path is only comparable on the platform it was evaluated for.
		return releaseChange{true, fmt.Sprintf("%s is for %s, not the engine's %s", tag, platform, engine)}, nil
	}

	current, err := m.ImageOutPath(ctx, profiles)
	if err != nil {
		return releaseChange{}, err
	}
	if current != recorded {
		return releaseChange{true, "image differs from " + tag + "'s"}, nil
	}
	return releaseChange{false, "image unchanged since " + tag}, nil
}
