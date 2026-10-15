package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"dagger/lib/cachetest"

	// Sets DAGGER_SESSION_PORT/TOKEN before internal/dagger's init() reads
	// them. Without it this test binary panics at startup.
	_ "dagger/lib/cacheprobe"
)

func TestEngineReleaseLabels(t *testing.T) {
	cachetest.RequireEngine(t)
	ctx := context.Background()
	_, artifact, err := artifactForTag("forgejo-resources-1.0.4")
	if err != nil {
		t.Fatal(err)
	}
	metadata := releaseMetadata{
		Revision:  strings.Repeat("a", 40),
		Created:   "2026-10-11T12:00:00Z",
		Changelog: "### Bug Fixes\n- Fix title label\n",
	}
	for _, authored := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", authored), func(t *testing.T) {
			container := dag.Container()
			wantTitle, wantDescription := artifact.Title, artifact.Description
			if authored {
				wantTitle, wantDescription = "forgejo-resources", "Reconcile users and repos."
				container = container.
					WithLabel("org.opencontainers.image.title", wantTitle).
					WithLabel("org.opencontainers.image.description", wantDescription).
					WithLabel("org.opencontainers.image.version", "dev")
			}
			container, err := withReleaseLabels(ctx, container, artifact, "forgejo-resources-1.0.4", metadata)
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{
				"org.opencontainers.image.title":       wantTitle,
				"org.opencontainers.image.description": wantDescription + "\n\nBug Fixes Fix title label",
				"org.opencontainers.image.version":     "1.0.4",
				"org.opencontainers.image.revision":    metadata.Revision,
			} {
				got, err := container.Label(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s: got %q, want %q", key, got, want)
				}
			}
		})
	}
}

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
		Changelog: "### Features\n- **ci:** Add tooling\n" + strings.Repeat("é", 600),
	}
	labels := releaseLabels(artifact, "devenv-ci-1.2.3", metadata, nil)
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
	if !utf8.ValidString(description) || len(description) > 512 || !strings.HasSuffix(description, "…") {
		t.Errorf("description exceeds GitHub's limit or has invalid truncation: %q", description)
	}
	if got := releaseDescription("Basic description", ""); got != "Basic description" {
		t.Errorf("empty changelog: %q", got)
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
		case "org.opencontainers.image.description":
			want += "\n\nBug Fixes Fix title label"
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

func TestReleaseDescriptionByteLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"ascii at limit", strings.Repeat("a", 512), strings.Repeat("a", 512)},
		{"ascii overflow", strings.Repeat("a", 513), strings.Repeat("a", 509) + "…"},
		{"unicode at limit", strings.Repeat("é", 256), strings.Repeat("é", 256)},
		{"unicode overflow", strings.Repeat("é", 257), strings.Repeat("é", 254) + "…"},
		{"emoji overflow", strings.Repeat("🙂", 129), strings.Repeat("🙂", 127) + "…"},
		{"mixed boundary", strings.Repeat("a", 508) + "🙂tail", strings.Repeat("a", 508) + "…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := releaseDescription(tc.input, "")
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) || len(got) > 512 {
				t.Errorf("invalid UTF-8 or oversized description: %q (%d bytes)", got, len(got))
			}
		})
	}
}
