// Builds, labels and publishes the repo's container images, as listed in
// releases.json, and tests the release scripts that tag them.

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"dagger/release/internal/dagger"

	"golang.org/x/sync/errgroup"
)

//go:embed releases.json
var releaseManifest []byte

const imageSource = "https://github.com/teekennedy/k8s-homelab"

type releaseArtifact struct {
	Prefix      string   `json:"prefix"`
	Profiles    []string `json:"profiles"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
}

type releaseMetadata struct {
	Tag       string `json:"tag"`
	Revision  string `json:"revision"`
	Created   string `json:"created"`
	Changelog string `json:"changelog"`
}

// imageLabels are the OCI labels every published image carries.
func imageLabels(title, description, documentation string) map[string]string {
	return map[string]string{
		"org.opencontainers.image.title":         title,
		"org.opencontainers.image.description":   description,
		"org.opencontainers.image.source":        imageSource,
		"org.opencontainers.image.url":           imageSource,
		"org.opencontainers.image.documentation": documentation,
		"org.opencontainers.image.licenses":      "MIT",
		"org.opencontainers.image.version":       "dev",
		"org.opencontainers.image.revision":      "unknown",
	}
}

// Keep plain-text descriptions within 512 UTF-8 bytes, including the ellipsis.
func releaseDescription(description, changelog string) string {
	var lines []string
	for line := range strings.SplitSeq(changelog, "\n") {
		line = strings.TrimLeft(strings.TrimSpace(line), "#- ")
		line = strings.NewReplacer("**", "", "`", "").Replace(line)
		if line != "" {
			lines = append(lines, strings.Join(strings.Fields(line), " "))
		}
	}
	if len(lines) > 0 {
		description += "\n\n" + strings.Join(lines, " ")
	}
	if len(description) > 512 {
		const ellipsis = "…"
		end := 512 - len(ellipsis)
		for !utf8.RuneStart(description[end]) {
			end--
		}
		return strings.TrimSpace(description[:end]) + ellipsis
	}
	return description
}

// authoredLabelFields are the OCI labels an image may set itself.
var authoredLabelFields = []string{"title", "description", "source", "url", "documentation", "licenses"}

func releaseLabels(artifact releaseArtifact, tag string, metadata releaseMetadata, existing map[string]string) map[string]string {
	labels := imageLabels(artifact.Title, artifact.Description,
		"https://git.msng.to/ops/k8s-homelab/releases/tag/"+tag)
	// Image-authored metadata takes precedence over release manifest defaults.
	for _, field := range authoredLabelFields {
		key := "org.opencontainers.image." + field
		if value, ok := existing[key]; ok {
			labels[key] = value
		}
	}
	labels["org.opencontainers.image.description"] = releaseDescription(labels["org.opencontainers.image.description"], metadata.Changelog)
	labels["org.opencontainers.image.version"] = strings.TrimPrefix(tag, artifact.Prefix+"-")
	if metadata.Revision != "" {
		labels["org.opencontainers.image.revision"] = metadata.Revision
		labels["org.opencontainers.image.created"] = metadata.Created
	}
	if metadata.Changelog != "" {
		labels["io.github.teekennedy.homelab.changelog"] = metadata.Changelog
	}
	return labels
}

func withReleaseLabels(ctx context.Context, container *dagger.Container, artifact releaseArtifact, tag string, metadata releaseMetadata) (*dagger.Container, error) {
	existing := make(map[string]string, len(authoredLabelFields))
	for _, field := range authoredLabelFields {
		key := "org.opencontainers.image." + field
		value, err := container.Label(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("read image label %s: %w", key, err)
		}
		if value != "" {
			existing[key] = value
		}
	}
	return withImageLabels(container, releaseLabels(artifact, tag, metadata, existing)), nil
}

// withImageLabels sets labels, and the OCI ones as manifest annotations too.
func withImageLabels(container *dagger.Container, labels map[string]string) *dagger.Container {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		container = container.WithLabel(key, labels[key])
		if strings.HasPrefix(key, "org.opencontainers.image.") {
			container = container.WithAnnotation(key, labels[key])
		}
	}
	return container
}

type releaseProject struct {
	Directory string            `json:"directory"`
	Artifacts []releaseArtifact `json:"artifacts"`
}

var releaseVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func artifactForTag(tag string) (releaseProject, releaseArtifact, error) {
	var projects map[string]releaseProject
	if err := json.Unmarshal(releaseManifest, &projects); err != nil {
		return releaseProject{}, releaseArtifact{}, fmt.Errorf("read release manifest: %w", err)
	}
	for _, project := range projects {
		for _, artifact := range project.Artifacts {
			version, ok := strings.CutPrefix(tag, artifact.Prefix+"-")
			if ok && releaseVersionPattern.MatchString(version) {
				return project, artifact, nil
			}
		}
	}
	return releaseProject{}, releaseArtifact{}, fmt.Errorf("invalid container release tag: %q", tag)
}

// Image builds a tagged container with OCI metadata, without publishing it.
func (m *Release) Image(
	ctx context.Context,
	ws dagger.Workspace,
	tag string,
	// JSON emitted by scripts/release-metadata.sh, including the tag's git-cliff notes.
	// +optional
	metadata *dagger.File,
) (*dagger.Container, error) {
	project, artifact, err := artifactForTag(tag)
	if err != nil {
		return nil, err
	}
	var release releaseMetadata
	if metadata != nil {
		contents, err := metadata.Contents(ctx)
		if err != nil {
			return nil, fmt.Errorf("read release metadata: %w", err)
		}
		if err := json.Unmarshal([]byte(contents), &release); err != nil {
			return nil, fmt.Errorf("parse release metadata: %w", err)
		}
		if release.Tag != tag {
			return nil, fmt.Errorf("metadata tag %q does not match %q", release.Tag, tag)
		}
		if !regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`).MatchString(release.Revision) {
			return nil, fmt.Errorf("invalid release revision %q", release.Revision)
		}
		if _, err := time.Parse(time.RFC3339, release.Created); err != nil {
			return nil, fmt.Errorf("invalid release creation time: %w", err)
		}
	}
	var container *dagger.Container
	if len(artifact.Profiles) > 0 {
		container = m.nix().Devenv().Container(dagger.NixDevenvContainerOpts{Profiles: artifact.Profiles})
	} else {
		container = ws.Directory(project.Directory, dagger.WorkspaceDirectoryOpts{Gitignore: true}).DockerBuild()
	}
	return withReleaseLabels(ctx, container, artifact, tag, release)
}

// Publish selects the container and profiles from the release tag and pushes to GHCR.
func (m *Release) Publish(
	ctx context.Context,
	ws dagger.Workspace,
	tag string,
	username string,
	token *dagger.Secret,
	// JSON emitted by scripts/release-metadata.sh.
	// +optional
	metadata *dagger.File,
) (string, error) {
	container, err := m.Image(ctx, ws, tag, metadata)
	if err != nil {
		return "", err
	}
	address, err := container.WithRegistryAuth("ghcr.io", username, token).
		Publish(ctx, "ghcr.io/"+username+"/k8s-homelab:"+tag)
	if err != nil {
		return "", fmt.Errorf("publish %s: %w", tag, err)
	}
	return address, nil
}

// Builds every Dockerfile in the repo.
//
// Each builds with its own directory as the context, so each keeps an
// independent build cache.
// +check
func (m *Release) Build(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) error {
	files, err := source.Glob(ctx, "**/Dockerfile")
	if err != nil {
		return fmt.Errorf("discover Dockerfiles: %w", err)
	}
	g := new(errgroup.Group)
	for _, file := range files {
		g.Go(func() error {
			_, err := directoryAt(source, filepath.Dir(file)).DockerBuild().Sync(ctx)
			if err != nil {
				return fmt.Errorf("build %s: %w", file, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("build Dockerfiles: %w", err)
	}
	return nil
}

// directoryAt is base's subdirectory at path, or base itself for ".", which
// Directory does not accept.
func directoryAt(base *dagger.Directory, path string) *dagger.Directory {
	if path == "." {
		return base
	}
	return base.Directory(path)
}

// withExecOutput appends a failed exec's output to its error, which otherwise
// only carries the exit code.
func withExecOutput(err error) error {
	if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
		return fmt.Errorf("%w\n%s%s", err, execErr.Stdout, execErr.Stderr)
	}
	return err
}
