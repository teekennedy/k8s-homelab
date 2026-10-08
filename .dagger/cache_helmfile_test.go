package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"dagger/homelab/internal/dagger"
	"dagger/homelab/internal/daggerfake"
)

// The helmfile half of the cache-granularity table: the fixture, checks and
// scenarios for BuildHelmfile and ValidateHelmfile. The harness that runs them
// is in cache_test.go.
//
// LintHelmfile is not in the table. It is one exec per environment rather than
// a fan-out, and it parses that exec's stdout, which neither backend produces.
// Its cross-checking logic is covered directly by TestHelmfileStateProblems.

// ---------------------------------------------------------------------------
// the helmfile fixture
// ---------------------------------------------------------------------------

// helmfileFixture mirrors the real repo's helmfile layout: the state file, one
// generated values file per environment, a chart in each of two tiers, and a
// shared library chart. See goFixture for what nonce is for.
func helmfileFixture(nonce string) repo {
	chart := func(name string) string {
		return "apiVersion: v2\nname: " + name + "\nversion: 0.1.0 # " + nonce + "\n"
	}
	env := func(name string) string { return `{"name": "` + name + `", "nonce": "` + nonce + `"}` + "\n" }
	return repo{
		"devenv.nix":  "{ } # " + nonce + "\n",
		"devenv.yaml": "imports:\n  - ./cmd/lab\n",
		"devenv.lock": "{}\n",

		helmfilePath:                     "releases: [] # " + nonce + "\n",
		"config/gen/production/env.json": env("production"),
		"config/gen/staging/env.json":    env("staging"),

		"k8s/apps/alpha/Chart.yaml":  chart("alpha"),
		"k8s/apps/alpha/values.yaml": "replicas: 1\n",

		// The per-environment overlay only the foundation tier has.
		"k8s/foundation/beta/Chart.yaml":      chart("beta"),
		"k8s/foundation/beta/values.yaml":     "replicas: 1\n",
		"k8s/foundation/beta/production.yaml": "replicas: 3\n",

		// A library chart: mounted into every release, never one itself.
		"k8s/charts/shared/Chart.yaml": chart("shared"),
	}
}

// helmfileEnvironments is what the checks are invoked with, so the table can
// ask about caching per environment as well as per chart.
var helmfileEnvironments = []string{"production", "staging"}

// The unit names for helmfileFixture. A unit is one release in one environment,
// named "<environment>.<release>" — see unitByHelmfileRelease.
const (
	prodAlpha    = "production.alpha"
	prodBeta     = "production.beta"
	stagingAlpha = "staging.alpha"
	stagingBeta  = "staging.beta"

	// The units the "adding a chart" scenario grows.
	prodAdded    = "production.gamma"
	stagingAdded = "staging.gamma"
)

var helmfileUnits = []string{prodAlpha, prodBeta, stagingAlpha, stagingBeta}

// helmfileUnitFiles is what each unit must have mounted at /repo: the state
// file, its own environment's values, its own chart and the library charts —
// and neither the other chart nor the other environment.
var helmfileUnitFiles = map[string][]string{
	prodAlpha: {
		"config/gen/production/env.json", helmfilePath,
		"k8s/apps/alpha/Chart.yaml", "k8s/apps/alpha/values.yaml", "k8s/charts/shared/Chart.yaml",
	},
	stagingAlpha: {
		"config/gen/staging/env.json", helmfilePath,
		"k8s/apps/alpha/Chart.yaml", "k8s/apps/alpha/values.yaml", "k8s/charts/shared/Chart.yaml",
	},
	prodBeta: {
		"config/gen/production/env.json", helmfilePath, "k8s/charts/shared/Chart.yaml",
		"k8s/foundation/beta/Chart.yaml", "k8s/foundation/beta/production.yaml", "k8s/foundation/beta/values.yaml",
	},
	stagingBeta: {
		"config/gen/staging/env.json", helmfilePath, "k8s/charts/shared/Chart.yaml",
		"k8s/foundation/beta/Chart.yaml", "k8s/foundation/beta/production.yaml", "k8s/foundation/beta/values.yaml",
	},
}

// addedHelmChart is the chart the "adding a chart" scenario grows.
var addedHelmChart = map[string]string{
	"k8s/apps/gamma/Chart.yaml":  "apiVersion: v2\nname: gamma\nversion: 0.1.0\n",
	"k8s/apps/gamma/values.yaml": "replicas: 1\n",
}

// ---------------------------------------------------------------------------
// checks
// ---------------------------------------------------------------------------

func buildHelmfileCheck() check {
	return check{
		name:      "build-helmfile",
		fixture:   helmfileFixture,
		scenarios: helmfileScenarios,
		invoke: func(ctx context.Context, m *Homelab, source *dagger.Directory, ctr *dagger.Container) error {
			_, err := m.BuildHelmfile(ctx, source, helmfileEnvironments, nil, ctr)
			return err
		},
		unitOf: unitByHelmfileRelease,
		mount:  strings.TrimPrefix(helmRepoRoot, "/"),
		wantUnitArgv: func(unit string) []string {
			return []string{helmfileTemplateArgv(unit)}
		},
		wantUnitFiles: helmfileUnitFiles,
		shimTools:     []string{"helmfile"},
		shimMarker:    markerFromHelmfileArgs,
	}
}

func validateHelmfileCheck() check {
	return check{
		name:      "validate-helmfile",
		fixture:   helmfileFixture,
		scenarios: helmfileScenarios,
		invoke: func(ctx context.Context, m *Homelab, source *dagger.Directory, ctr *dagger.Container) error {
			_, err := m.ValidateHelmfile(ctx, source, helmfileEnvironments, nil, ctr)
			return err
		},
		unitOf: unitByHelmfileRelease,
		mount:  strings.TrimPrefix(helmRepoRoot, "/"),
		// The render comes first because lint runs on its output: that is what
		// lets it skip the dependency build.
		wantUnitArgv: func(unit string) []string {
			env, release, _ := strings.Cut(unit, ".")
			return []string{
				helmfileTemplateArgv(unit),
				"helmfile --environment " + env + " --selector name=" + release + " lint --skip-deps",
			}
		},
		wantUnitFiles: helmfileUnitFiles,
		shimTools:     []string{"helmfile"},
		shimMarker:    markerFromHelmfileArgs,
	}
}

// helmfileTemplateArgv is the render every helmfile unit starts with.
func helmfileTemplateArgv(unit string) string {
	env, release, _ := strings.Cut(unit, ".")
	return `sh -c exec "$@" > /rendered.yaml -- helmfile --environment ` + env +
		" --selector name=" + release + " template --include-crds"
}

// unitByHelmfileRelease names a helmfile check's unit from its argv, as
// "<environment>.<release>". Unlike the Go checks, the argv is what tells one
// unit from another here: every release gets its own --environment and
// --selector. That the mount is scoped to match is asserted separately, by
// helmfileUnitFiles. Execs that are not a helmfile invocation return "".
func unitByHelmfileRelease(e daggerfake.Exec) string {
	if !slices.Contains(e.Args, "helmfile") {
		return ""
	}
	var env, release string
	for i, arg := range e.Args {
		if arg == "--environment" && i+1 < len(e.Args) {
			env = e.Args[i+1]
		}
		if name, ok := strings.CutPrefix(arg, "name="); ok {
			release = name
		}
	}
	if env == "" || release == "" {
		return ""
	}
	return env + "." + release
}

// markerFromHelmfileArgs is the engine backend's shimMarker for the helmfile
// checks: the shim standing in for helmfile reads the same two arguments
// unitByHelmfileRelease does.
const markerFromHelmfileArgs = `$(e=; n=; while [ $# -gt 0 ]; do case "$1" in ` +
	`--environment) e=$2 ;; name=*) n=${1#name=} ;; esac; shift; done; echo "$e.$n")`

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

// helmfileScenarios is the table both helmfile checks name as their
// `scenarios`: they fan out per release and environment over the same fixture.
func helmfileScenarios() []scenario {
	return []scenario{
		{
			// The core assertion: one chart's edit re-renders that chart, in
			// each environment, and nothing else.
			name:   "editing one chart invalidates only that chart",
			edit:   edit("k8s/apps/alpha/values.yaml", "replicas: 2\n"),
			rerun:  []string{prodAlpha, stagingAlpha},
			cached: []string{prodBeta, stagingBeta},
		},
		{
			// Each release mounts only its own environment's values, so
			// regenerating one environment leaves the others alone.
			name:   "editing one environment's values invalidates only that environment",
			edit:   edit("config/gen/staging/env.json", `{"name": "staging", "edited": true}`+"\n"),
			rerun:  []string{stagingAlpha, stagingBeta},
			cached: []string{prodAlpha, prodBeta},
		},
		{
			// Discovery growing a new chart must not itself invalidate the
			// charts already there.
			name:   "adding a chart leaves the existing charts cached",
			edit:   addFiles(addedHelmChart),
			rerun:  []string{prodAdded, stagingAdded},
			cached: helmfileUnits,
		},
		{
			// Expected, not a defect: the state file defines every release, and
			// helmfile reads all of it whichever release is selected.
			name:  "editing the state file invalidates every release",
			edit:  edit(helmfilePath, "releases: [] # edited\n"),
			rerun: helmfileUnits,
		},
		{
			// Also expected: the library charts are mounted into every release
			// rather than only into the charts that depend on them.
			name:  "editing a library chart invalidates every release",
			edit:  edit("k8s/charts/shared/Chart.yaml", "apiVersion: v2\nname: shared\nversion: 0.2.0\n"),
			rerun: helmfileUnits,
		},
		{
			// The negative control for every "toolchain stayed cached" above.
			name:      "editing devenv.nix rebuilds the toolchain, and so every unit",
			edit:      edit("devenv.nix", "{ packages = [ ]; }\n"),
			rerun:     helmfileUnits,
			toolchain: toolchainRebuilt,
		},
	}
}

// ---------------------------------------------------------------------------
// LintHelmfile and path matching
// ---------------------------------------------------------------------------

func TestHelmfileStateProblems(t *testing.T) {
	const envValues = "config/gen/production/env.json"
	charts := []string{"k8s/apps/alpha", "k8s/foundation/beta"}
	releases := []helmfileListEntry{
		{Name: "alpha", Chart: "k8s/apps/alpha"},
		{Name: "beta", Chart: "k8s/foundation/beta"},
	}
	apps := map[string]map[string]bool{
		"apps": {"alpha": true},
		// Listed but disabled is still listed.
		"foundation": {"beta": false},
	}

	tests := []struct {
		name     string
		releases []helmfileListEntry
		charts   []string
		apps     map[string]map[string]bool
		want     []string
	}{
		{
			name:     "releases, charts and apps agree",
			releases: releases,
			charts:   charts,
			apps:     apps,
		},
		{
			name:     "a chart with no release",
			releases: releases[:1],
			charts:   charts,
			apps:     apps,
			want:     []string{"chart k8s/foundation/beta has no release in " + helmfilePath},
		},
		{
			name:     "a release with no chart",
			releases: releases,
			charts:   charts[:1],
			apps:     apps,
			want:     []string{`release "beta" points at k8s/foundation/beta, which is not a chart`},
		},
		{
			name:     "a release missing from the environment's apps",
			releases: releases,
			charts:   charts,
			apps:     map[string]map[string]bool{"apps": {"alpha": true}},
			want:     []string{`release "beta" is not listed under apps.foundation in ` + envValues},
		},
		{
			// An app key with no release is tolerated: it deploys nothing.
			name:     "an app with no release",
			releases: releases,
			charts:   charts,
			apps: map[string]map[string]bool{
				"apps":       {"alpha": true, "retired": true},
				"foundation": {"beta": false},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := helmfileStateProblems(tt.releases, tt.charts, tt.apps, envValues)
			if !slices.Equal(got, tt.want) {
				t.Errorf("want %q\ngot  %q", tt.want, got)
			}
		})
	}
}

func TestMatchHelmfilePaths(t *testing.T) {
	charts := []string{"k8s/apps/alpha", "k8s/foundation/beta"}

	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{"a file in one chart", []string{"k8s/apps/alpha/values.yaml"}, charts[:1]},
		{"the state file", []string{helmfilePath}, charts},
		{"generated environment values", []string{"config/gen/production/env.json"}, charts},
		{"a library chart", []string{"k8s/charts/shared/Chart.yaml"}, charts},
		{"an unrelated file", []string{"nix/hosts/common/default.nix"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchHelmfilePaths(tt.paths, charts); !slices.Equal(got, tt.want) {
				t.Errorf("want %v, got %v", tt.want, got)
			}
		})
	}
}
