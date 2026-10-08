package main

import (
	"context"
	"fmt"
	"strings"

	"dagger/homelab/internal/dagger"
	"dagger/homelab/internal/daggerfake"
)

// The Go half of the cache-granularity table: the fixture, checks and scenarios
// for TestGo and LintGo. The harness that runs them is in cache_test.go.

// ---------------------------------------------------------------------------
// the Go fixture
// ---------------------------------------------------------------------------

// goFixture mirrors the real repo's Go layout: the lab CLI under cmd/lab, two
// unrelated modules under k8s/, and a vendored module that discovery must skip.
// Each module carries a distinctly named file so its exec can be identified by
// what is mounted into it.
//
// nonce is woven into every file so a run can start from a cold cache. The fake
// backend passes "" — it is a fresh engine every time — while the engine
// backend passes a per-process value, so that a previous invocation's cache
// entries can never make run 1 look like a hit.
func goFixture(nonce string) repo {
	body := func(pkg string) string { return "package " + pkg + " // " + nonce + "\n\nfunc main() {}\n" }
	return repo{
		"devenv.nix":  "{ } # " + nonce + "\n",
		"devenv.yaml": "imports:\n  - ./cmd/lab\n",
		"devenv.lock": "{}\n",

		".golangci.yaml": "version: \"2\"\n",

		"cmd/lab/go.mod": "module lab\n\ngo 1.26\n",
		"cmd/lab/lab.go": body("main"),

		"k8s/apps/homepage/files/secret-sync/go.mod":         "module secret-sync\n\ngo 1.26\n",
		"k8s/apps/homepage/files/secret-sync/secretsync.go":  body("main"),
		"k8s/platform/forgejo/files/config/go.mod":           "module config\n\ngo 1.26\n",
		"k8s/platform/forgejo/files/config/forgejoconfig.go": body("main"),

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
	addedUnit      = "tool.go"

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
}

// addedGoModule is the module the "adding a module" scenarios grow.
var addedGoModule = map[string]string{
	"k8s/apps/newthing/files/tool/go.mod":  "module newthing\n\ngo 1.26\n",
	"k8s/apps/newthing/files/tool/tool.go": "package main\n\nfunc main() {}\n",
}

func testGoCheck() check {
	return check{
		name:      "test-go",
		fixture:   goFixture,
		scenarios: goScenarios,
		invoke: func(ctx context.Context, m *Homelab, source *dagger.Directory, ctr *dagger.Container) error {
			_, err := m.TestGo(ctx, source, ctr)
			return err
		},
		unitOf:        unitByScopedGoModule,
		mount:         srcMount,
		wantUnitArgv:  sameArgv("go test ./..."),
		wantUnitFiles: goUnitFiles,
		shimTools:     []string{"go"},
		shimMarker:    markerFromMount,
	}
}

func lintGoCheck() check {
	return check{
		name:      "lint-go",
		fixture:   goFixture,
		scenarios: goScenarios,
		invoke: func(ctx context.Context, m *Homelab, source *dagger.Directory, ctr *dagger.Container) error {
			changes, err := m.LintGo(ctx, source, ctr)
			if err != nil {
				return err
			}
			// The changeset is lazy, and merging it is the part of LintGo that
			// only reaches the engine when something is selected off it.
			// IsEmpty is what forces that; Changeset.sync does not.
			if _, err := changes.IsEmpty(ctx); err != nil {
				return fmt.Errorf("forcing LintGo's changeset: %w", err)
			}
			return nil
		},
		unitOf: unitByScopedGoModule,
		mount:  srcMount,
		wantUnitArgv: sameArgv(
			"go mod tidy",
			"golangci-lint run --fix --config "+golangciLintConfigPath+" ./...",
		),
		wantUnitFiles: goUnitFiles,
		shimTools:     []string{"go", "golangci-lint"},
		shimMarker:    markerFromMount,
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
// being mistaken for its unit name. Execs with no such file — the devenv
// toolchain build, anything running on an earlier exec's output — are not
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
// paths in goFixture, and repo.with() fatals on a path that isn't there.
func goScenarios() []scenario {
	return []scenario{
		{
			// The core assertion. Editing one module's source must leave every
			// other module's work untouched. The module edited here lives
			// outside cmd/lab; see "editing cmd/lab rebuilds everything" below
			// for why that matters.
			name:   "editing one module invalidates only that module",
			edit:   edit(secretSyncPath, "package main // edited\n\nfunc main() { println(1) }\n"),
			rerun:  []string{secretSyncUnit},
			cached: []string{labUnit, forgejoUnit},
		},
		{
			// The same question from the other end of the tree, and the one
			// that guards the DevenvSource scoping in New(): a Go file under
			// k8s/ must not reach the toolchain container.
			name:   "editing a module outside cmd/lab keeps the toolchain cached",
			edit:   edit("k8s/platform/forgejo/files/config/forgejoconfig.go", "package main\n\nfunc main() { _ = 1 }\n"),
			rerun:  []string{forgejoUnit},
			cached: []string{labUnit, secretSyncUnit},
		},
		{
			// Discovery growing a new module must not itself be a
			// cache-invalidating event for the modules already there.
			name:   "adding a module leaves the existing modules cached",
			edit:   addFiles(addedGoModule),
			rerun:  []string{addedUnit},
			cached: []string{labUnit, secretSyncUnit, forgejoUnit},
		},
		{
			// The negative control for every "toolchain stayed cached" above:
			// it proves the toolchain execs really are keyed on DevenvSource
			// rather than being inert, so a green result there means something.
			name:      "editing devenv.nix rebuilds the toolchain, and so every unit",
			edit:      edit("devenv.nix", "{ packages = [ ]; }\n"),
			rerun:     []string{labUnit, secretSyncUnit, forgejoUnit},
			toolchain: toolchainRebuilt,
		},
		{
			// This scenario records behaviour that is NOT what this module is
			// trying to achieve. It passes today; if it starts failing, the
			// coupling below has been fixed and it should be replaced by an
			// entry with cmd/lab in `cached` and the default toolchain want.
			//
			// New()'s +ignore pulls all of cmd/lab into DevenvSource, because
			// devenv.yaml imports ./cmd/lab as a devenv module. That module's
			// cmd/lab/default.nix builds the lab CLI with `src = ./.`, and
			// cmd/lab/devenv.nix puts the result in `packages` unconditionally
			// — so the ci profile's shell closure contains a Nix build of every
			// Go file under cmd/lab.
			//
			// The result: editing any of cmd/lab's ~20 Go files rebuilds the
			// devenv shell, which invalidates the container that every single
			// check in this module runs in. Not just the Go checks — LintYaml,
			// ValidateWoodpecker, the Helm and Python and Terraform checks too.
			// It is the widest cache invalidation the module has.
			//
			// Fixing it means breaking the tie between "the toolchain devenv
			// builds" and "the lab CLI's source": move `packages = [lab]` into
			// the interactive profile so the ci profile never realises the CLI
			// derivation, then narrow New()'s +ignore to the Nix files under
			// cmd/lab rather than all of cmd/lab.
			name:      "editing cmd/lab rebuilds the toolchain, and so every unit",
			edit:      edit("cmd/lab/lab.go", "package main\n\nfunc main() { println(\"changed\") }\n"),
			rerun:     []string{labUnit, secretSyncUnit, forgejoUnit},
			toolchain: toolchainRebuilt,
		},
	}
}
