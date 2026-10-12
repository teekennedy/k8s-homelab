package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"dagger/homelab/internal/dagger"
	"golang.org/x/sync/errgroup"
)

//go:embed releases.json
var releaseManifest []byte

type releaseArtifact struct {
	Prefix   string   `json:"prefix"`
	Profiles []string `json:"profiles"`
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

// PublishRelease selects the container and profiles from the release tag and pushes to GHCR.
func (m *Homelab) PublishRelease(
	ctx context.Context,
	ws dagger.Workspace,
	tag string,
	username string,
	token *dagger.Secret,
) (string, error) {
	project, artifact, err := artifactForTag(tag)
	if err != nil {
		return "", err
	}
	var container *dagger.Container
	if len(artifact.Profiles) > 0 {
		container = m.DevenvContainer(ws, nil, artifact.Profiles)
	} else {
		container = ws.Directory(project.Directory, dagger.WorkspaceDirectoryOpts{Gitignore: true}).DockerBuild()
	}
	address, err := container.WithRegistryAuth("ghcr.io", username, token).
		Publish(ctx, "ghcr.io/"+username+"/k8s-homelab:"+tag)
	if err != nil {
		return "", fmt.Errorf("publish %s: %w", tag, err)
	}
	return address, nil
}

// BuildDockerfiles builds every Dockerfile using its containing directory as the build context.
// +check
func (m *Homelab) BuildDockerfiles(
	ctx context.Context,
	// +defaultPath="/"
	source *dagger.Directory,
) (string, error) {
	files, err := source.Glob(ctx, "**/Dockerfile")
	if err != nil {
		return "", fmt.Errorf("discover Dockerfiles: %w", err)
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
		return "", fmt.Errorf("build Dockerfiles: %w", err)
	}
	return fmt.Sprintf("Built %d Dockerfiles", len(files)), nil
}
