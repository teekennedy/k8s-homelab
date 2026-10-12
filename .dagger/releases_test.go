package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestArtifactForTag(t *testing.T) {
	for _, tc := range []struct {
		tag       string
		directory string
		profiles  []string
	}{
		{"devenv-ci-1.2.3", ".", []string{"ci"}},
		{"devenv-shell-1.2.3", ".", []string{"ci", "interactive"}},
		{"forgejo-resources-1.0.0", "k8s/platform/forgejo/files/forgejo-resources", nil},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			project, artifact, err := artifactForTag(tc.tag)
			if err != nil {
				t.Fatal(err)
			}
			if project.Directory != tc.directory || !reflect.DeepEqual(artifact.Profiles, tc.profiles) {
				t.Fatalf("got directory %q, profiles %v", project.Directory, artifact.Profiles)
			}
		})
	}
	for _, tag := range []string{"devenv-1.2.3", "devenv-ci-01.2.3", "devenv-shell-1.2", "forgejo-resources-1.0.0-rc1", "unknown-1.0.0"} {
		if _, _, err := artifactForTag(tag); err == nil {
			t.Errorf("accepted invalid tag %q", tag)
		}
	}
}

func TestReleaseLabels(t *testing.T) {
	_, artifact, err := artifactForTag("devenv-ci-1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	metadata := releaseMetadata{
		Revision:  strings.Repeat("a", 40),
		Created:   "2026-10-11T12:00:00Z",
		Changelog: "### Features\n- **ci:** Add tooling\n" + strings.Repeat("é", 600),
	}
	labels := releaseLabels(artifact, "devenv-ci-1.2.3", metadata)
	for key, want := range map[string]string{
		"org.opencontainers.image.source":        imageSource,
		"org.opencontainers.image.licenses":      "MIT",
		"org.opencontainers.image.version":       "1.2.3",
		"org.opencontainers.image.revision":      metadata.Revision,
		"org.opencontainers.image.created":       metadata.Created,
		"org.opencontainers.image.documentation": "https://git.msng.to/ops/k8s-homelab/releases/tag/devenv-ci-1.2.3",
		"io.github.teekennedy.homelab.changelog": metadata.Changelog,
	} {
		if labels[key] != want {
			t.Errorf("%s: got %q, want %q", key, labels[key], want)
		}
	}
	description := labels["org.opencontainers.image.description"]
	if !strings.HasPrefix(description, "Devenv shell CI profile\n\nFeatures ci: Add tooling") {
		t.Errorf("description lacks profile and plain-text changelog: %q", description)
	}
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > 512 || !strings.HasSuffix(description, "…") {
		t.Errorf("description exceeds GitHub's limit or has invalid truncation: %q", description)
	}
	if got := releaseDescription("Basic description", ""); got != "Basic description" {
		t.Errorf("empty changelog: %q", got)
	}
}
