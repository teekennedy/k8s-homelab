package main

import (
	"context"
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

// The cache-granularity questions for the workflow. The cluster itself cannot be
// modelled without an engine, so these cover the two places a cache key is
// decided before any cluster exists: the source RunKubernetesIntegration is
// called with, and the dependency install that precedes the cluster.

func integrationFixture() repo {
	return repo{
		helmfilePath:                      "releases: []\n",
		"config/gen/ephemeral/env.json":   "{\"name\": \"ephemeral\"}\n",
		"config/gen/production/env.json":  "{\"name\": \"production\"}\n",
		"k8s/charts/shared/Chart.yaml":    "name: shared\n",
		"k8s/foundation/alpha/Chart.yaml": "name: alpha\n",
		"k8s/foundation/alpha/tests/t.py": "def test_a(): ...\n",
		"k8s/foundation/beta/Chart.yaml":  "name: beta\n",
		"k8s/foundation/beta/tests/t.py":  "def test_b(): ...\n",
		"k8s/apps/gamma/Chart.yaml":       "name: gamma\n",
	}
}

func scopedSourceID(t *testing.T, files repo, chosen ...string) string {
	t.Helper()
	var plan []*integrationRelease
	for _, chart := range chosen {
		plan = append(plan, &integrationRelease{Chart: chart})
	}
	id, err := integrationSource(files.directory(), plan).ID(context.Background())
	if err != nil {
		t.Fatalf("integrationSource: %v", err)
	}
	return string(id)
}

func TestIntegrationSourceCacheGranularity(t *testing.T) {
	const alpha = "k8s/foundation/alpha"
	tests := []struct {
		name        string
		edit        func(*testing.T, repo) repo
		chosen      []string
		wantChanged bool
	}{
		{"a chart that is not deployed", edit("k8s/foundation/beta/Chart.yaml", "name: beta2\n"), []string{alpha}, false},
		{"a test of a chart that is not deployed", edit("k8s/foundation/beta/tests/t.py", "x\n"), []string{alpha}, false},
		{"an app in another tier", edit("k8s/apps/gamma/Chart.yaml", "name: gamma2\n"), []string{alpha}, false},
		{"another environment's values", edit("config/gen/production/env.json", "{}\n"), []string{alpha}, false},
		{"a deployed chart", edit("k8s/foundation/alpha/Chart.yaml", "name: alpha2\n"), []string{alpha}, true},
		{"a deployed release's test", edit("k8s/foundation/alpha/tests/t.py", "x\n"), []string{alpha}, true},
		{"the shared charts", edit("k8s/charts/shared/Chart.yaml", "name: shared2\n"), []string{alpha}, true},
		{"the state file", edit(helmfilePath, "releases: [x]\n"), []string{alpha}, true},
		{"the ephemeral values", edit("config/gen/ephemeral/env.json", "{}\n"), []string{alpha}, true},
		{
			"a chart once it is deployed too",
			edit("k8s/foundation/beta/Chart.yaml", "name: beta2\n"),
			[]string{alpha, "k8s/foundation/beta"},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeEngine(t)
			base := integrationFixture()
			before := scopedSourceID(t, base, tt.chosen...)
			after := scopedSourceID(t, tt.edit(t, base), tt.chosen...)
			if changed := before != after; changed != tt.wantChanged {
				t.Errorf("scoped source changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

// Choosing different releases is a different argument, so it must not be served
// the other's result.
func TestIntegrationSourceDependsOnChosenReleases(t *testing.T) {
	fakeEngine(t)
	files := integrationFixture()
	if scopedSourceID(t, files, "k8s/foundation/alpha") == scopedSourceID(t, files, "k8s/foundation/beta") {
		t.Error("different releases produced the same scoped source")
	}
}

func TestKubernetesEnvCacheGranularity(t *testing.T) {
	project := func() repo {
		return repo{
			"pyproject.toml": "[project]\nname = \"t\"\n",
			"uv.lock":        "version = 1\n",
			"test_a.py":      "def test_a(): ...\n",
			"conftest.py":    "\n",
		}
	}
	installKey := func(t *testing.T, files repo) string {
		t.Helper()
		engine := fakeEngine(t)
		pp := &PythonProject{Path: "tests", Source: files.directory()}
		if _, err := pp.kubernetesEnv(dag.Container().From("toolchain")).Sync(context.Background()); err != nil {
			t.Fatalf("kubernetesEnv: %v", err)
		}
		for _, e := range engine.Execs() {
			if len(e.Args) > 0 && e.Args[0] == "uv" {
				return e.CacheKey
			}
		}
		t.Fatal("kubernetesEnv ran no uv exec")
		return ""
	}

	tests := []struct {
		name        string
		edit        func(*testing.T, repo) repo
		wantChanged bool
	}{
		{"a test", edit("test_a.py", "def test_a(): pass\n"), false},
		{"the fixtures", edit("conftest.py", "import os\n"), false},
		{"a new test file", addFiles(map[string]string{"test_b.py": "x\n"}), false},
		{"the lock file", edit("uv.lock", "version = 2\n"), true},
		{"the project metadata", edit("pyproject.toml", "[project]\nname = \"u\"\n"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := project()
			before := installKey(t, base)
			after := installKey(t, tt.edit(t, base))
			if changed := before != after; changed != tt.wantChanged {
				t.Errorf("dependency install re-ran = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

// Close is deferred before Create, so it has to cope with a cluster that never
// started.
func TestCloseBeforeCreate(t *testing.T) {
	if err := (&k3sCluster{Name: "homelab-abcdef"}).Close(context.Background()); err != nil {
		t.Errorf("Close on a cluster that never started: %v", err)
	}
}
