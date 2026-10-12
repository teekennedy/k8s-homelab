package main

import (
	"reflect"
	"testing"
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
