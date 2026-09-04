package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"dagger/homelab/internal/dagger"
	"dagger/homelab/internal/daggerfake"

	// Sets DAGGER_SESSION_PORT/TOKEN before internal/dagger's init() reads
	// them. Without it this test binary panics at startup. See the package doc.
	_ "dagger/homelab/internal/cacheprobe"
)

// This file asserts the property the module is structured around: for any given
// source change, `dagger check` should re-run the minimum set of execs.
//
// It does that without an engine. The Go SDK is lazy, so a check's whole call
// chain reaches the engine as one GraphQL query; internal/daggerfake stands in
// for the engine, evaluates that query against a content-addressed model of
// Directory/File/Container, and records a cache key per WithExec. Two runs that
// produce the same key for an exec are two runs where `dagger check` would
// print CACHED. See the daggerfake package doc for the model's limits.
//
// # Adding coverage for another check
//
// Everything below is table-driven around two types. A `check` says how to
// invoke one module function and how to read its work back — which execs are
// its per-unit work, and what names each unit. A `scenario` says what to edit
// and which units must then re-run. To cover a new check:
//
//  1. Write a fixture: a `repo` (path -> contents) laid out like the real tree,
//     with a distinctly named file per unit so units can be told apart.
//  2. Write a scenario table against that fixture. It has to be its own table:
//     a scenario's edits name paths in the fixture, and repo.with() fatals on a
//     path that isn't there, so goScenarios cannot be pointed at a Python tree.
//  3. Add a `check` value wiring the two together, and list it in allChecks().
//
// Nothing else has to change here: the shape test, both backends and the
// toolchain assertions are all driven off the `check`.
//
// The two backends live in their own files — backend_fake_test.go models the
// engine, backend_engine_test.go drives a real one — so this file stays what is
// being asserted rather than how it is observed. What may have to change
// is the check's own module function — the fake backend needs to pass nil for
// the toolchain container and the engine backend a shim, so the function needs
// the optional `container` parameter that TestGo and LintGo have and that
// LintPython, ValidateHelm and ValidateTerraform currently do not.

// ---------------------------------------------------------------------------
// the workspace under test
// ---------------------------------------------------------------------------

// repo is a source tree, as path-to-contents.
type repo map[string]string

// with returns a copy of the repo with one file's contents replaced. It fails
// the test if the path isn't already present, so a typo reads as a typo rather
// than as a passing "nothing was invalidated".
func (r repo) with(t *testing.T, path, contents string) repo {
	t.Helper()
	if _, ok := r[path]; !ok {
		t.Fatalf("repo has no file %q to edit", path)
	}
	return r.plus(map[string]string{path: contents})
}

// plus returns a copy of the repo with extra files added.
func (r repo) plus(extra map[string]string) repo {
	next := make(repo, len(r)+len(extra))
	maps.Copy(next, r)
	maps.Copy(next, extra)
	return next
}

// directory materialises the repo as a Dagger directory.
func (r repo) directory() *dagger.Directory {
	d := dag.Directory()
	for _, p := range sortedKeys(r) {
		d = d.WithNewFile(p, r[p])
	}
	return d
}

// edit returns a scenario edit that replaces one file's contents.
func edit(path, contents string) func(*testing.T, repo) repo {
	return func(t *testing.T, r repo) repo {
		t.Helper()
		return r.with(t, path, contents)
	}
}

// addFiles returns a scenario edit that adds files to the tree.
func addFiles(extra map[string]string) func(*testing.T, repo) repo {
	return func(_ *testing.T, r repo) repo { return r.plus(extra) }
}

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

// ---------------------------------------------------------------------------
// checks
// ---------------------------------------------------------------------------

// check is one module function these tests can drive, together with everything
// needed to read its work back: how to invoke it, which execs make up its
// per-unit work, and how the engine backend observes those execs.
type check struct {
	// name identifies the check in test names and failure messages.
	name string

	// fixture builds the source tree this check runs against. nonce is woven in
	// by the backend; see goFixture.
	fixture func(nonce string) repo

	// scenarios are the cache-granularity questions to ask of this check. They
	// live on the check because a scenario's edits name paths in the check's own
	// fixture, so a table cannot be shared across checks with different fixtures.
	// Checks that fan out the same way over the same fixture do share one, as
	// the two Go checks share goScenarios.
	scenarios func() []scenario

	// invoke calls the module function. container is the toolchain to run in:
	// the fake backend passes nil (so the check builds the real devenv chain,
	// lazily, which is what the toolchain assertions inspect), the engine
	// backend passes a shim.
	invoke func(ctx context.Context, m *Homelab, source *dagger.Directory, container *dagger.Container) error

	// unitOf names the unit of work an exec belongs to, or "" for an exec that
	// is shared setup rather than per-unit work.
	unitOf func(daggerfake.Exec) string

	// wantUnitArgv is the argv every unit must run, in order. It pins the shape
	// the cache assertions rest on: a check that stopped fanning out per unit,
	// or grew a step, is caught here rather than silently weakening every
	// scenario below.
	wantUnitArgv []string

	// wantUnitFiles is what each unit must have mounted at /src, by unit name.
	wantUnitFiles map[string][]string

	// shimTools are the executables the engine backend replaces with a stamping
	// shim, and shimMarker is the shell snippet that names the unit from inside
	// the exec. See engine_cache_test.go.
	shimTools  []string
	shimMarker string
}

// allChecks is the registry the shape test and the scenario tables iterate.
func allChecks() []check { return []check{testGoCheck(), lintGoCheck()} }

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
		wantUnitArgv:  []string{"go test ./..."},
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
		wantUnitArgv: []string{
			"go mod tidy",
			"golangci-lint run --fix --config " + golangciLintConfigPath + " ./...",
		},
		wantUnitFiles: goUnitFiles,
		shimTools:     []string{"go", "golangci-lint"},
		shimMarker:    markerFromMount,
	}
}

// srcMount is where every check mounts the tree it works on.
const srcMount = "src"

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

// homelab builds the module receiver the way New() does, with DevenvSource
// scoped by the same patterns as its +ignore: the devenv files, plus all of
// cmd/lab, which devenv.yaml imports as a devenv module.
func homelab(files repo) *Homelab {
	devenv := repo{}
	for p, c := range files {
		if strings.HasPrefix(p, "devenv.") || strings.HasPrefix(p, "cmd/lab/") {
			devenv[p] = c
		}
	}
	return &Homelab{DevenvSource: devenv.directory()}
}

// ---------------------------------------------------------------------------
// backends
// ---------------------------------------------------------------------------

// units maps a unit of work to an identity that stays the same exactly when
// that unit's work would be served from cache. Two identities compared across
// two runs answer the only question these tests ask: would `dagger check`
// re-run this unit, or print CACHED?
type units map[string]string

// outcome is one run of a check against one source tree.
type outcome struct {
	units units
	// toolchain identifies the shared devenv container build, or "" on a
	// backend that does not build one.
	toolchain string
	// execs is the raw exec log, for failure messages and the shape test. It is
	// empty on the engine backend, which observes runs rather than describing
	// them.
	execs []daggerfake.Exec
}

// backend runs a check against a source tree and reports, per unit, an identity
// that changes exactly when that unit's work would re-run.
//
// There are two implementations, one per file. fakeBackend
// (backend_fake_test.go) derives identities from internal/daggerfake's modelled
// cache keys, needs no engine, and runs in milliseconds. engineBackend
// (backend_engine_test.go) runs the same scenarios against a real engine and
// observes which execs actually ran. The scenario table is shared, so the two
// are held to the same assertions.
type backend interface {
	// name identifies the backend in failure messages.
	name() string
	// fixture returns the source tree to start from.
	fixture(c check) repo
	// run invokes the check and reports what ran.
	run(t *testing.T, c check, files repo) outcome
	// buildsToolchain reports whether run() can answer toolchain questions.
	buildsToolchain() bool
}

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

// toolchainWant says what an edit must do to the shared devenv container.
type toolchainWant int

const (
	// toolchainCached is the default and the one that matters: the devenv build
	// is the most expensive step in any check, and every check mounts it, so a
	// source edit that rebuilds it costs far more than the check it triggered.
	toolchainCached toolchainWant = iota
	// toolchainRebuilt asserts the opposite. A scenario that wants it is asking
	// a question only a backend that builds a toolchain can answer, so the
	// engine backend — which runs against a shim container — skips it.
	toolchainRebuilt
)

// scenario is one cache-granularity question: start from the check's fixture,
// make an edit, and say which units must then re-run.
type scenario struct {
	// name identifies the scenario; it becomes the subtest name.
	name string

	// edit transforms the fixture into the tree for the second run.
	edit func(*testing.T, repo) repo

	// rerun names the units whose work must re-run after the edit. A unit that
	// only exists after the edit counts as re-run.
	rerun []string

	// cached names the units whose work must be served from cache. Every unit
	// the check produces has to appear in exactly one of rerun and cached, so a
	// fixture that grows a module cannot silently go unasserted.
	cached []string

	// toolchain is what the edit must do to the shared devenv container.
	toolchain toolchainWant
}

func (s scenario) run(t *testing.T, b backend, c check) {
	t.Helper()

	if s.toolchain == toolchainRebuilt && !b.buildsToolchain() {
		t.Skipf("[%s] this scenario is about the devenv toolchain, which this backend does not build", b.name())
	}

	base := b.fixture(c)
	first := b.run(t, c, base)
	second := b.run(t, c, s.edit(t, base))

	s.assertUnits(t, b, first, second)
	s.assertToolchain(t, b, first, second)
}

func (s scenario) assertUnits(t *testing.T, b backend, first, second outcome) {
	t.Helper()
	s.assertRerun(t, b, first, second)
	s.assertCached(t, b, first, second)
	s.assertNothingUnasserted(t, b, first, second)
}

func (s scenario) assertRerun(t *testing.T, b backend, first, second outcome) {
	t.Helper()
	for _, unit := range s.rerun {
		before, existed := first.units[unit]
		after, ok := second.units[unit]
		switch {
		case !ok:
			t.Errorf("[%s] unit %q did not run at all after the edit; units that ran: %v",
				b.name(), unit, sortedKeys(second.units))
		case existed && before == after:
			t.Errorf("[%s] unit %q was served from cache, but the edit should have invalidated it\n%s",
				b.name(), unit, formatExecs(second.execs))
		}
	}
}

func (s scenario) assertCached(t *testing.T, b backend, first, second outcome) {
	t.Helper()
	for _, unit := range s.cached {
		before, existed := first.units[unit]
		after, ok := second.units[unit]
		switch {
		case !existed:
			t.Errorf("[%s] unit %q never ran on the first run; the scenario cannot say it stayed cached",
				b.name(), unit)
		case !ok:
			t.Errorf("[%s] unit %q disappeared after the edit; units that ran: %v",
				b.name(), unit, sortedKeys(second.units))
		case before != after:
			t.Errorf("[%s] unit %q was re-run, but nothing it owns changed; caching is not per-unit\n"+
				"before: %s\nafter:  %s", b.name(), unit, before, after)
		}
	}
}

// assertNothingUnasserted makes every unit the check produced appear in exactly
// one of rerun and cached. Without it a scenario could pass by staying silent
// about the module that actually broke — including one a fixture grew later.
func (s scenario) assertNothingUnasserted(t *testing.T, b backend, first, second outcome) {
	t.Helper()

	asserted := map[string]bool{}
	for _, u := range append(append([]string(nil), s.rerun...), s.cached...) {
		asserted[u] = true
	}
	for _, seen := range []units{first.units, second.units} {
		for unit := range seen {
			if !asserted[unit] {
				t.Errorf("[%s] unit %q ran but the scenario says nothing about it; "+
					"add it to rerun or cached", b.name(), unit)
				asserted[unit] = true
			}
		}
	}
}

func (s scenario) assertToolchain(t *testing.T, b backend, first, second outcome) {
	t.Helper()
	if !b.buildsToolchain() {
		return
	}
	if first.toolchain == "" {
		t.Fatalf("[%s] no devenv toolchain execs recorded; the container build changed shape", b.name())
	}

	switch s.toolchain {
	case toolchainCached:
		if first.toolchain != second.toolchain {
			t.Errorf("[%s] the edit rebuilt the devenv toolchain container, invalidating every check "+
				"in the module\nbefore: %s\nafter:  %s", b.name(), first.toolchain, second.toolchain)
		}
	case toolchainRebuilt:
		if first.toolchain == second.toolchain {
			t.Errorf("[%s] the edit did not invalidate the toolchain container; "+
				"the model may not be keying on DevenvSource at all", b.name())
		}
	}
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

// ---------------------------------------------------------------------------
// the tests
// ---------------------------------------------------------------------------

// TestCacheGranularity runs every scenario against every check, on the fake
// backend.
func TestCacheGranularity(t *testing.T) {
	for _, c := range allChecks() {
		t.Run(c.name, func(t *testing.T) {
			for _, s := range c.scenarios() {
				t.Run(s.name, func(t *testing.T) {
					s.run(t, newFakeBackend(), c)
				})
			}
		})
	}
}

// TestEngineCacheGranularity runs the same table against a real engine, and
// skips without a Dagger session. See backend_engine_test.go for how a cache
// hit is observed there.
func TestEngineCacheGranularity(t *testing.T) {
	for _, c := range allChecks() {
		t.Run(c.name, func(t *testing.T) {
			for _, s := range c.scenarios() {
				t.Run(s.name, func(t *testing.T) {
					s.run(t, newEngineBackend(t), c)
				})
			}
		})
	}
}

// TestEngineOracleObservesExecs is the control for every scenario above: it
// proves the shim actually records runs and that a repeat of identical work is
// a cache hit. Without it, a backend that silently recorded nothing would make
// every isolation assertion pass.
func TestEngineOracleObservesExecs(t *testing.T) {
	for _, c := range allChecks() {
		t.Run(c.name, func(t *testing.T) {
			b := newEngineBackend(t)
			files := b.fixture(c)

			first := b.run(t, c, files).units
			if len(first) == 0 {
				t.Fatal("the shim recorded no execs at all; the oracle is not observing anything")
			}
			for unit := range c.wantUnitFiles {
				if first[unit] == "" {
					t.Errorf("no exec recorded for unit %q; got %v", unit, sortedKeys(first))
				}
			}

			// Re-running byte-identical work must change nothing.
			second := b.run(t, c, files).units
			if d := delta(first, second); len(d) != 0 {
				t.Errorf("re-running an unchanged tree re-executed %v; the engine is not caching at all", d)
			}
		})
	}
}

// delta returns the entries whose count changed between two reads. Scenarios
// compare identities rather than deltas; this is only used above and in failure
// messages.
func delta(before, after units) []string {
	var changed []string
	for k, v := range after {
		if before[k] != v {
			changed = append(changed, fmt.Sprintf("%s %s->%s", k, before[k], v))
		}
	}
	return changed
}

// ---------------------------------------------------------------------------
// shape
// ---------------------------------------------------------------------------

// TestChecksFanOutPerUnit pins the shape the whole caching argument rests on:
// one run of the check's steps per unit, each with only that unit's own files
// mounted. If this drifts — a single exec over the whole tree, say — no amount
// of cache assertions above would mean anything.
func TestChecksFanOutPerUnit(t *testing.T) {
	for _, c := range allChecks() {
		t.Run(c.name, func(t *testing.T) {
			execs := newFakeBackend().run(t, c, c.fixture("")).execs

			byUnit := map[string][]string{}
			for _, e := range execs {
				if unit := c.unitOf(e); unit != "" {
					byUnit[unit] = append(byUnit[unit], strings.Join(e.Args, " "))
				}
			}

			if len(byUnit) != len(c.wantUnitFiles) {
				t.Fatalf("want %d units, got %d (%v)\n%s",
					len(c.wantUnitFiles), len(byUnit), sortedKeys(byUnit), formatExecs(execs))
			}

			for unit, wantFiles := range c.wantUnitFiles {
				gotArgv, ok := byUnit[unit]
				if !ok {
					t.Errorf("no execs for unit %q\n%s", unit, formatExecs(execs))
					continue
				}
				if strings.Join(gotArgv, " | ") != strings.Join(c.wantUnitArgv, " | ") {
					t.Errorf("unit %q runs the wrong steps\nwant: %v\ngot:  %v",
						unit, c.wantUnitArgv, gotArgv)
				}
				if got := unitFiles(execs, c, unit); strings.Join(got, ",") != strings.Join(wantFiles, ",") {
					t.Errorf("unit %q has the wrong files mounted at /%s\nwant: %v\ngot:  %v",
						unit, srcMount, wantFiles, got)
				}
			}
		})
	}
}

// unitFiles returns the files mounted at /src for a unit's first exec.
func unitFiles(execs []daggerfake.Exec, c check, unit string) []string {
	for _, e := range execs {
		if c.unitOf(e) == unit {
			return e.Files[srcMount]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func formatExecs(execs []daggerfake.Exec) string {
	var b strings.Builder
	for _, e := range execs {
		b.WriteString("  ")
		b.WriteString(e.Label())
		b.WriteString("\n")
		for _, mount := range sortedKeys(e.Mounts) {
			b.WriteString("      mount ")
			b.WriteString(mount)
			b.WriteString(" = ")
			b.WriteString(e.Mounts[mount])
			b.WriteString("\n")
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
