package main

import (
	"slices"
	"strings"
	"testing"
)

// What the workflow decides before it creates anything: which releases the
// environment it is about to deploy with has turned on, and whether the ones
// asked for are among them. Helmfile's installedTemplate reads enablement from
// the environment's values, so a --selector can narrow a sync but cannot turn a
// release on — which makes the environment the only usable source of truth.

func TestEnabledReleases(t *testing.T) {
	apps := map[string]map[string]bool{
		"foundation": {"reflector": true, "traefik": false},
		"platform":   {"forgejo": false},
		"apps":       {"homepage": true},
	}
	// Sorted, so the deploy order and the summary don't depend on Go's map
	// iteration order.
	want := []string{"homepage", "reflector"}
	if got := enabledReleases(apps); !slices.Equal(got, want) {
		t.Errorf("enabledReleases() = %q, want %q", got, want)
	}
	if got := enabledReleases(nil); got != nil {
		t.Errorf("enabledReleases(nil) = %q, want none", got)
	}
}

func TestCheckEnabled(t *testing.T) {
	tests := []struct {
		name      string
		enabled   []string
		requested []string
		wantErr   []string
	}{
		{
			name:      "every requested release is enabled",
			enabled:   []string{"reflector", "secret-system"},
			requested: []string{"reflector"},
		},
		{
			name:      "the whole enabled set, which is the default",
			enabled:   []string{"reflector", "secret-system"},
			requested: []string{"reflector", "secret-system"},
		},
		{
			// Without this the sync silently installs nothing and the tests run
			// against a cluster missing the thing they test.
			name:      "a requested release the environment disables",
			enabled:   []string{"secret-system"},
			requested: []string{"reflector"},
			wantErr:   []string{"does not enable reflector", "it enables secret-system"},
		},
		{
			name:      "a release that is not in the state file at all",
			enabled:   []string{"reflector"},
			requested: []string{"reflector", "typo"},
			wantErr:   []string{"does not enable typo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkEnabled(tt.enabled, tt.requested)
			if len(tt.wantErr) == 0 {
				if err != nil {
					t.Fatalf("checkEnabled: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkEnabled accepted enabled=%q requested=%q, want an error",
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

func TestIndent(t *testing.T) {
	got := indent("first\nsecond\n", "  ")
	want := "  first\n  second"
	if got != want {
		t.Errorf("indent() = %q, want %q", got, want)
	}
}

func TestReleaseVerdict(t *testing.T) {
	release := &integrationRelease{
		Name:  "reflector",
		Tests: &PythonProject{Path: "k8s/foundation/reflector/tests"},
	}

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
			wantLine:   []string{`no tests marked "kubernetes" were collected`, release.Tests.Path},
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
