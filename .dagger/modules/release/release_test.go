package main

import (
	"reflect"
	"strings"
	"testing"

	// Sets DAGGER_SESSION_PORT/TOKEN before internal/dagger's init() reads
	// them. Without it this test binary panics at startup.
	_ "dagger/lib/cacheprobe"
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
		{"openhands-sandbox-0.2.0", "k8s/apps/openhands/files/sandbox", nil},
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
		Changelog: "### Features\n- **ci:** Add tooling\n",
	}
	labels := releaseLabels(artifact, "devenv-ci-1.2.3", metadata, nil)
	for key, want := range map[string]string{
		"org.opencontainers.image.description":   "Devenv shell CI profile",
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
}

func TestReleaseLabelsPreserveImageMetadata(t *testing.T) {
	_, artifact, err := artifactForTag("forgejo-resources-1.0.4")
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]string{
		"org.opencontainers.image.title":         "forgejo-resources",
		"org.opencontainers.image.description":   "Reconcile Forgejo users and repos.",
		"org.opencontainers.image.source":        "https://example.com/source",
		"org.opencontainers.image.url":           "https://example.com",
		"org.opencontainers.image.documentation": "https://example.com/docs",
		"org.opencontainers.image.licenses":      "",
		"org.opencontainers.image.version":       "dev",
		"org.opencontainers.image.revision":      "unknown",
		"org.opencontainers.image.created":       "old timestamp",
	}
	metadata := releaseMetadata{
		Revision:  strings.Repeat("a", 40),
		Created:   "2026-10-11T12:00:00Z",
		Changelog: "### Bug Fixes\n- Fix title label\n",
	}
	labels := releaseLabels(artifact, "forgejo-resources-1.0.4", metadata, existing)
	for key, want := range existing {
		switch key {
		case "org.opencontainers.image.version":
			want = "1.0.4"
		case "org.opencontainers.image.revision":
			want = metadata.Revision
		case "org.opencontainers.image.created":
			want = metadata.Created
		}
		if labels[key] != want {
			t.Errorf("%s: got %q, want %q", key, labels[key], want)
		}
	}
	delete(existing, "org.opencontainers.image.title")
	labels = releaseLabels(artifact, "forgejo-resources-1.0.4", metadata, existing)
	if labels["org.opencontainers.image.title"] != artifact.Title {
		t.Error("missing title did not fall back to the manifest")
	}
}
