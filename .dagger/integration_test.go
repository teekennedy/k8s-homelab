package main

import (
	"strings"
	"testing"
)

// What the workflow decides before it creates anything: that the environment it
// is about to deploy with enables exactly the releases under test. Helmfile's
// installedTemplate reads that from the environment's values, so a --selector
// can narrow a sync but cannot turn a release on or off — which makes this the
// only place the two can be reconciled.

func TestEnablementProblems(t *testing.T) {
	const values = "config/gen/ephemeral/env.json"

	tests := []struct {
		name      string
		enabled   []string
		requested []string
		wantErr   []string
	}{
		{
			name:      "the environment enables exactly what is under test",
			enabled:   []string{"reflector"},
			requested: []string{"reflector"},
		},
		{
			name:      "several releases, in a different order",
			enabled:   []string{"secret-system", "reflector"},
			requested: []string{"reflector", "secret-system"},
		},
		{
			name:      "a release under test that the environment disables",
			enabled:   []string{},
			requested: []string{"reflector"},
			wantErr:   []string{"reflector was asked for but is not enabled"},
		},
		{
			// The expensive mistake: an unrelated release left enabled gets
			// deployed, dragging in credentials the ephemeral cluster has none of.
			name:      "a release the environment enables that nobody asked for",
			enabled:   []string{"reflector", "forgejo"},
			requested: []string{"reflector"},
			wantErr:   []string{"forgejo is enabled in " + values + " but was not asked for"},
		},
		{
			name:      "both at once",
			enabled:   []string{"forgejo"},
			requested: []string{"reflector"},
			wantErr: []string{
				"forgejo is enabled",
				"reflector was asked for but is not enabled",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := enablementProblems(tt.enabled, tt.requested, values)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("enablementProblems: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("enablementProblems accepted enabled=%q requested=%q, want an error",
					tt.enabled, tt.requested)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

// The default is what `dagger call test-kubernetes-integration` with no
// --releases runs, and it has to agree with the ephemeral environment's own
// defaults or every call fails the enablement check.
func TestDefaultIntegrationReleasesMatchTheEphemeralEnvironment(t *testing.T) {
	if err := enablementProblems(defaultIntegrationReleases, defaultIntegrationReleases, "n/a"); err != nil {
		t.Fatalf("the default releases disagree with themselves: %v", err)
	}
	if len(defaultIntegrationReleases) == 0 {
		t.Fatal("defaultIntegrationReleases is empty, so the workflow would deploy nothing")
	}
}

func TestIndent(t *testing.T) {
	got := indent("first\nsecond\n", "  ")
	want := "  first\n  second"
	if got != want {
		t.Errorf("indent() = %q, want %q", got, want)
	}
}

func TestReleaseVerdict(t *testing.T) {
	release := &integrationRelease{Name: "reflector", TestsPath: "k8s/foundation/reflector/tests"}

	tests := []struct {
		name       string
		exitCode   int
		junit      string
		wantFailed bool
		wantLine   []string
	}{
		{
			name:     "every test passed",
			junit:    greenJUnit,
			wantLine: []string{"reflector: 2 tests, 0 failed"},
		},
		{
			name:     "a test failed",
			exitCode: 1,
			junit: `<testsuites><testsuite name="pytest" tests="2" failures="1">` +
				`<testcase classname="test_reflection" name="test_a">` +
				`<failure message="AssertionError: timed out">tb</failure></testcase>` +
				`</testsuite></testsuites>`,
			wantFailed: true,
			wantLine:   []string{"pytest exit code 1", "test_reflection::test_a failed"},
		},
		{
			// pytest exits 5 having collected nothing. The workflow asked for
			// this release's kubernetes tests, so collecting none means they
			// never ran — the outcome that must not read as a pass.
			name:       "nothing was collected",
			exitCode:   pytestNoTestsExitCode,
			junit:      `<testsuites><testsuite name="pytest" tests="0"/></testsuites>`,
			wantFailed: true,
			wantLine:   []string{`no tests marked "kubernetes" were collected`, release.TestsPath},
		},
		{
			// A clean exit code with an empty report is still nothing run.
			name:       "a clean exit with nothing collected",
			junit:      `<testsuites><testsuite name="pytest" tests="0"/></testsuites>`,
			wantFailed: true,
			wantLine:   []string{"no tests marked"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := parseJUnitReport(tt.junit)
			if err != nil {
				t.Fatalf("parseJUnitReport: %v", err)
			}
			line, failed := releaseVerdict(release, &pytestRun{ExitCode: tt.exitCode}, report)
			if failed != tt.wantFailed {
				t.Errorf("releaseVerdict failed = %v, want %v (line: %s)", failed, tt.wantFailed, line)
			}
			for _, want := range tt.wantLine {
				if !strings.Contains(line, want) {
					t.Errorf("summary line %q, want it to mention %q", line, want)
				}
			}
		})
	}
}
