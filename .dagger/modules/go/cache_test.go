package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"dagger/go/internal/dagger"
	"dagger/lib/cachetest"
	"dagger/lib/daggerfake"
)

// The cache-granularity table for go:test and go:lint: the fixture, checks and
// scenarios. The harness that runs them is dagger/lib/cachetest.

// TestCacheGranularity runs the Go checks through the cache harness.
func TestCacheGranularity(t *testing.T) {
	cachetest.Run(t, client{}, []string{"devenv"}, testCheck(), lintCheck())
}

// ---------------------------------------------------------------------------
// the Go fixture
// ---------------------------------------------------------------------------

// goFixture mirrors the real repo's Go layout: the lab CLI under cmd/lab, two
// unrelated modules under k8s/, a Dagger module building against .dagger/lib
// through a local replace, and a vendored module that discovery must skip.
// Each module carries a distinctly named file so its exec can be identified by
// what is mounted into it.
//
// nonce is woven into every file so a run can start from a cold cache. The fake
// backend passes "" — it is a fresh engine every time — while the engine
// backend passes a per-process value, so that a previous invocation's cache
// entries can never make run 1 look like a hit.
func goFixture(nonce string) cachetest.Repo {
	body := "package main // " + nonce + "\n\nfunc main() {}\n"
	return cachetest.Repo{
		".golangci.yaml": "version: \"2\"\n",

		"cmd/lab/go.mod": "module lab\n\ngo 1.26\n",
		"cmd/lab/lab.go": body,

		"k8s/apps/homepage/files/secret-sync/go.mod":                    "module secret-sync\n\ngo 1.26\n",
		"k8s/apps/homepage/files/secret-sync/secretsync.go":             body,
		"k8s/platform/forgejo/files/forgejo-resources/go.mod":           "module config\n\ngo 1.26\n",
		"k8s/platform/forgejo/files/forgejo-resources/forgejoconfig.go": body,

		// A module that builds against a sibling through a local replace, as
		// the Dagger modules here do with .dagger/lib.
		".dagger/lib/go.mod":           "module dagger/lib\n\ngo 1.26\n",
		libPath:                        "package lib // " + nonce + "\n\nconst X = 1\n",
		".dagger/modules/tool/go.mod":  "module dagger/tool\n\ngo 1.26\n\nrequire dagger/lib v0.0.0\n\nreplace dagger/lib => ../../lib\n",
		".dagger/modules/tool/tool.go": body,

		// Vendored: a go.mod, but not one of the repo's own modules.
		"cmd/lab/vendor/example.com/dep/go.mod": "module example.com/dep\n",
		"cmd/lab/vendor/example.com/dep/dep.go": "package dep // " + nonce + "\n",
	}
}

// The unit names for goFixture. A Go check scopes each module to its own mount,
// so a unit is named by the one .go file at the root of that mount — see
// unitByScopedGoModule.
const (
	labUnit        = "lab.go"
	secretSyncUnit = "secretsync.go"
	forgejoUnit    = "forgejoconfig.go"
	toolUnit       = "tool.go"
	libUnit        = "lib.go"
	addedUnit      = "added.go"

	// libPath is the local replace target the replace scenario edits.
	libPath = ".dagger/lib/lib.go"

	// secretSyncPath is the file the isolation scenarios edit.
	//nolint:gosec // G101 reads this fixture path as a credential; it is a file name
	secretSyncPath = "k8s/apps/homepage/files/secret-sync/secretsync.go"
)

// goUnitFiles is what each unit must have mounted at /src: its own files and
// nothing from its siblings. cmd/lab additionally carries its vendor tree,
// which rides along inside the module directory by design.
var goUnitFiles = map[string][]string{
	labUnit:        {"go.mod", "lab.go", "vendor/example.com/dep/dep.go", "vendor/example.com/dep/go.mod"},
	secretSyncUnit: {"go.mod", "secretsync.go"},
	forgejoUnit:    {"forgejoconfig.go", "go.mod"},
	toolUnit:       {"go.mod", "tool.go"},
	libUnit:        {"go.mod", "lib.go"},
}

// addedGoModule is the module the "adding a module" scenarios grow.
var addedGoModule = map[string]string{
	"k8s/apps/newthing/files/added/go.mod":   "module newthing\n\ngo 1.26\n",
	"k8s/apps/newthing/files/added/added.go": "package main\n\nfunc main() {}\n",
}

type goCheck = cachetest.Check[*dagger.Directory, *dagger.Container]

func testCheck() goCheck {
	return goCheck{
		Name:      "test",
		Fixture:   goFixture,
		Scenarios: goScenarios,
		Invoke: func(ctx context.Context, source *dagger.Directory, ctr *dagger.Container) error {
			_, err := New(ctr).Test(ctx, source)
			return err
		},
		UnitOf:        unitByScopedGoModule,
		Mount:         srcMount,
		WantUnitArgv:  cachetest.SameArgv("go test ./..."),
		WantUnitFiles: goUnitFiles,
		ShimTools:     []string{"go"},
		ShimMarker:    markerFromMount,
	}
}

func lintCheck() goCheck {
	return goCheck{
		Name:      "lint",
		Fixture:   goFixture,
		Scenarios: goScenarios,
		Invoke: func(ctx context.Context, source *dagger.Directory, ctr *dagger.Container) error {
			changes, err := New(ctr).Lint(ctx, source)
			if err != nil {
				return err
			}
			// The changeset is lazy, and merging it is the part of Lint that
			// only reaches the engine when something is selected off it.
			// IsEmpty is what forces that; Changeset.sync does not.
			if _, err := changes.IsEmpty(ctx); err != nil {
				return fmt.Errorf("forcing Lint's changeset: %w", err)
			}
			return nil
		},
		UnitOf: unitByScopedGoModule,
		Mount:  srcMount,
		WantUnitArgv: cachetest.SameArgv(
			"go mod tidy",
			"golangci-lint run --fix --config "+golangciLintConfigPath+" ./...",
		),
		WantUnitFiles: goUnitFiles,
		ShimTools:     []string{"go", "golangci-lint"},
		ShimMarker:    markerFromMount,
	}
}

// srcMount is where the Go checks mount the module they work on.
const srcMount = "src"

// markerFromMount is the engine backend's shimMarker for the Go checks. It
// names the module by the one .go file at the root of what is mounted at /src
// — how GoModule.Test and GoModule.Lint scope each module, and what
// unitByScopedGoModule reads on the fake side.
const markerFromMount = `$(basename "$(ls /src/*.go 2>/dev/null | head -1)")`

// unitByScopedGoModule names a Go check's unit by the one .go file sitting
// directly in the module directory mounted at /src. That is how one module's
// work is told apart from another's: every module runs the same argv in the
// same workdir, and only the mounted source differs.
//
// Nested paths are skipped, which is what keeps cmd/lab's vendored dep from
// being mistaken for its unit name. Execs with no such file — anything
// running on an earlier exec's output — are not
// per-unit work and return "". The engine backend's shim applies the same rule
// with `ls /src/*.go`, so both backends key their results identically.
func unitByScopedGoModule(e daggerfake.Exec) string {
	for _, f := range e.Files[srcMount] {
		if !strings.Contains(f, "/") && strings.HasSuffix(f, ".go") {
			return f
		}
	}
	return ""
}

// goScenarios is the table both Go checks name as their `scenarios`. They fan
// out per module over the same fixture, so the same questions apply to both.
// A check over a different fixture needs its own table: the edits below name
// paths in goFixture, and Repo.With fatals on a path that isn't there.
func goScenarios() []cachetest.Scenario {
	return []cachetest.Scenario{
		{
			// The core assertion. Editing one module's source must leave every
			// other module's work untouched.
			Name:   "editing one module invalidates only that module",
			Edit:   cachetest.Edit(secretSyncPath, "package main // edited\n\nfunc main() { println(1) }\n"),
			Rerun:  []string{secretSyncUnit},
			Cached: []string{labUnit, forgejoUnit, toolUnit, libUnit},
		},
		{
			// The same question from the other end of the tree.
			Name:   "editing another module invalidates only that module",
			Edit:   cachetest.Edit("k8s/platform/forgejo/files/forgejo-resources/forgejoconfig.go", "package main\n\nfunc main() { _ = 1 }\n"),
			Rerun:  []string{forgejoUnit},
			Cached: []string{labUnit, secretSyncUnit, toolUnit, libUnit},
		},
		{
			// Discovery growing a new module must not itself be a
			// cache-invalidating event for the modules already there.
			Name:   "adding a module leaves the existing modules cached",
			Edit:   cachetest.AddFiles(addedGoModule),
			Rerun:  []string{addedUnit},
			Cached: []string{labUnit, secretSyncUnit, forgejoUnit, toolUnit, libUnit},
		},
		{
			// A module that replaces another with a local path builds against
			// it, so it has to re-run when that target changes, and only it.
			Name:   "editing a local replace target invalidates it and the modules replacing it",
			Edit:   cachetest.Edit(libPath, "package lib\n\nconst X = 2\n"),
			Rerun:  []string{libUnit, toolUnit},
			Cached: []string{labUnit, secretSyncUnit, forgejoUnit},
		},
	}
}
