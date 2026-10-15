// Package cachetest asserts the property the toolchains under .dagger/modules
// are structured around: for any given source change, `dagger check` should
// re-run the minimum set of execs.
//
// It does that without an engine. The Go SDK is lazy, so a check's whole call
// chain reaches the engine as one GraphQL query; daggerfake stands in for the
// engine, evaluates that query against a content-addressed model of
// Directory/File/Container, and records a cache key per WithExec. Two runs that
// produce the same key for an exec are two runs where `dagger check` would
// print CACHED. See the daggerfake package doc for the model's limits.
//
// # Using it from a module
//
// The harness is generic over a module's generated *dagger.Directory and
// *dagger.Container types, because each module generates its own SDK. A module
// supplies a Client — a few lines over its own `dag` — and then, per check:
//
//  1. A fixture: a Repo (path -> contents) laid out like the real tree, with a
//     distinctly named file per unit so units can be told apart.
//  2. A scenario table against that fixture. It has to be its own table: a
//     scenario's edits name paths in the fixture, and Repo.With fatals on a
//     path that isn't there.
//  3. A Check wiring the two together, passed to Run.
//
// Run drives every scenario through both backends, plus a shape test and the
// engine control. The toolchain a check runs in is a constructor argument,
// wired from devenv in dagger.toml: the fake backend passes a stand-in for it,
// and the engine backend a shim.
//
// # The two backends
//
// The fake backend answers "would this unit re-run" from daggerfake's modelled
// cache keys rather than by running anything, so the whole table runs in
// milliseconds under a plain `go test`. The engine backend runs the same table
// against a real engine, and skips without a session; see engine.go.
package cachetest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"dagger/lib/daggerfake"

	"github.com/Khan/genqlient/graphql"
)

// ---------------------------------------------------------------------------
// the workspace under test
// ---------------------------------------------------------------------------

// Repo is a source tree, as path-to-contents.
type Repo map[string]string

// With returns a copy of the repo with one file's contents replaced. It fails
// the test if the path isn't already present, so a typo reads as a typo rather
// than as a passing "nothing was invalidated".
func (r Repo) With(t *testing.T, path, contents string) Repo {
	t.Helper()
	if _, ok := r[path]; !ok {
		t.Fatalf("repo has no file %q to edit", path)
	}
	return r.Plus(map[string]string{path: contents})
}

// Plus returns a copy of the repo with extra files added.
func (r Repo) Plus(extra map[string]string) Repo {
	next := make(Repo, len(r)+len(extra))
	maps.Copy(next, r)
	maps.Copy(next, extra)
	return next
}

// Edit returns a scenario edit that replaces one file's contents.
func Edit(path, contents string) func(*testing.T, Repo) Repo {
	return func(t *testing.T, r Repo) Repo {
		t.Helper()
		return r.With(t, path, contents)
	}
}

// AddFiles returns a scenario edit that adds files to the tree.
func AddFiles(extra map[string]string) func(*testing.T, Repo) Repo {
	return func(_ *testing.T, r Repo) Repo { return r.Plus(extra) }
}

// ---------------------------------------------------------------------------
// the module's side
// ---------------------------------------------------------------------------

// Client is what the harness needs from a module's generated SDK. D and C are
// that module's *dagger.Directory and *dagger.Container.
type Client[D, C any] interface {
	// UseFake points the module's `dag` at engine until the test ends. `dag` is
	// package-level state, so tests using it must not call t.Parallel().
	UseFake(t *testing.T, engine graphql.Client)
	// Toolchain returns a stand-in for the wired toolchain container.
	Toolchain() C
	// Directory builds files in-process with WithNewFile.
	Directory(files Repo) D
	// HostDirectory loads a host path the way the CLI loads a +defaultPath
	// argument, forcing it so an unreadable path fails here.
	HostDirectory(ctx context.Context, path string) (D, error)
	// Container returns image with volume mounted at mount and files written
	// executable, in order: the order is part of the container's identity, so
	// it has to be the same from one run to the next.
	Container(image, mount, volume string, files []File) C
	// Stdout runs script in Container(image, mount, volume, nil) with env set
	// to a fresh value, and returns its stdout.
	Stdout(ctx context.Context, image, mount, volume, env, script string) (string, error)
}

// File is an executable file the engine backend writes into a container.
type File struct {
	Path     string
	Contents string
}

// Check is one module function these tests can drive, together with everything
// needed to read its work back: how to invoke it, which execs are its per-unit
// work, and how the engine backend observes those execs.
type Check[D, C any] struct {
	// Name identifies the check in test names and failure messages.
	Name string

	// Fixture builds the source tree this check runs against. nonce is woven in
	// by the backend: the fake passes "", the engine a per-process value, so a
	// previous invocation's cache entries can never make run 1 look like a hit.
	Fixture func(nonce string) Repo

	// Scenarios are the cache-granularity questions to ask of this check.
	Scenarios func() []Scenario

	// Invoke calls the module function, for every item of a collection.
	// container is the toolchain to run in: a stand-in on the fake backend, a
	// shim on the engine backend.
	Invoke func(ctx context.Context, source D, container C) error

	// UnitOf names the unit of work an exec belongs to, or "" for an exec that
	// is shared setup rather than per-unit work.
	UnitOf func(daggerfake.Exec) string

	// Mount is where the check mounts the tree a unit works on, as a path with
	// no leading slash. Empty means each unit's working directory, for checks
	// that mount every unit at a path of its own.
	Mount string

	// WantUnitArgv is the argv a unit must run, in order. It pins the shape the
	// cache assertions rest on: a check that stopped fanning out per unit, or
	// grew a step, is caught here rather than silently weakening every
	// scenario.
	WantUnitArgv func(unit string) []string

	// WantUnitFiles is what each unit must have mounted at Mount, by unit name.
	WantUnitFiles map[string][]string

	// ShimTools are the executables the engine backend replaces with a stamping
	// shim, and ShimMarker is the shell snippet that names the unit from inside
	// the exec. See engine.go.
	ShimTools  []string
	ShimMarker string
}

// SameArgv is a WantUnitArgv for checks whose units all run the same commands
// and differ only in what is mounted.
func SameArgv(argv ...string) func(string) []string {
	return func(string) []string { return argv }
}

// ---------------------------------------------------------------------------
// scenarios
// ---------------------------------------------------------------------------

// Scenario is one cache-granularity question: start from the check's fixture,
// make an edit, and say which units must then re-run.
type Scenario struct {
	// Name identifies the scenario; it becomes the subtest name.
	Name string

	// Edit transforms the fixture into the tree for the second run.
	Edit func(*testing.T, Repo) Repo

	// Rerun names the units whose work must re-run after the edit. A unit that
	// only exists after the edit counts as re-run.
	Rerun []string

	// Cached names the units whose work must be served from cache. Every unit
	// the check produces has to appear in exactly one of Rerun and Cached, so a
	// fixture that grows a unit cannot silently go unasserted.
	Cached []string
}

// units maps a unit of work to an identity that stays the same exactly when
// that unit's work would be served from cache.
type units map[string]string

// outcome is one run of a check against one source tree.
type outcome struct {
	units units
	// execs is the raw exec log, for failure messages and the shape test. It is
	// empty on the engine backend, which observes runs rather than describing
	// them.
	execs []daggerfake.Exec
}

// backend runs a check against a source tree and reports, per unit, an identity
// that changes exactly when that unit's work would re-run.
type backend[D, C any] interface {
	name() string
	fixture(c Check[D, C]) Repo
	run(t *testing.T, c Check[D, C], files Repo) outcome
}

func (s Scenario) run(t *testing.T, b interface{ name() string }, first, second outcome) {
	t.Helper()
	s.assertRerun(t, b.name(), first, second)
	s.assertCached(t, b.name(), first, second)
	s.assertNothingUnasserted(t, b.name(), first, second)
}

func (s Scenario) assertRerun(t *testing.T, b string, first, second outcome) {
	t.Helper()
	for _, unit := range s.Rerun {
		before, existed := first.units[unit]
		after, ok := second.units[unit]
		switch {
		case !ok:
			t.Errorf("[%s] unit %q did not run at all after the edit; units that ran: %v",
				b, unit, sortedKeys(second.units))
		case existed && before == after:
			t.Errorf("[%s] unit %q was served from cache, but the edit should have invalidated it\n%s",
				b, unit, formatExecs(second.execs))
		}
	}
}

func (s Scenario) assertCached(t *testing.T, b string, first, second outcome) {
	t.Helper()
	for _, unit := range s.Cached {
		before, existed := first.units[unit]
		after, ok := second.units[unit]
		switch {
		case !existed:
			t.Errorf("[%s] unit %q never ran on the first run; the scenario cannot say it stayed cached",
				b, unit)
		case !ok:
			t.Errorf("[%s] unit %q disappeared after the edit; units that ran: %v",
				b, unit, sortedKeys(second.units))
		case before != after:
			t.Errorf("[%s] unit %q was re-run, but nothing it owns changed; caching is not per-unit\n"+
				"before: %s\nafter:  %s", b, unit, before, after)
		}
	}
}

// assertNothingUnasserted makes every unit the check produced appear in exactly
// one of Rerun and Cached. Without it a scenario could pass by staying silent
// about the unit that actually broke — including one a fixture grew later.
func (s Scenario) assertNothingUnasserted(t *testing.T, b string, first, second outcome) {
	t.Helper()

	asserted := map[string]bool{}
	for _, u := range append(append([]string(nil), s.Rerun...), s.Cached...) {
		asserted[u] = true
	}
	for _, seen := range []units{first.units, second.units} {
		for unit := range seen {
			if !asserted[unit] {
				t.Errorf("[%s] unit %q ran but the scenario says nothing about it; "+
					"add it to Rerun or Cached", b, unit)
				asserted[unit] = true
			}
		}
	}
}

// ---------------------------------------------------------------------------
// the tests
// ---------------------------------------------------------------------------

// Run is the whole suite for a set of checks: every scenario on the fake
// backend, the same table on a real engine (skipped without a session), the
// engine control, and the per-unit shape.
func Run[D, C any](t *testing.T, client Client[D, C], checks ...Check[D, C]) {
	t.Helper()
	fake := newFakeBackend(client)

	t.Run("CacheGranularity", func(t *testing.T) {
		forEachScenario(t, checks, func(t *testing.T, c Check[D, C], s Scenario) {
			runScenario(t, fake, c, s)
		})
	})
	t.Run("EngineCacheGranularity", func(t *testing.T) {
		forEachScenario(t, checks, func(t *testing.T, c Check[D, C], s Scenario) {
			runScenario[D, C](t, newEngineBackend(t, client), c, s)
		})
	})
	t.Run("EngineOracleObservesExecs", func(t *testing.T) {
		for _, c := range checks {
			t.Run(c.Name, func(t *testing.T) { engineOracle(t, newEngineBackend(t, client), c) })
		}
	})
	t.Run("ChecksFanOutPerUnit", func(t *testing.T) {
		for _, c := range checks {
			t.Run(c.Name, func(t *testing.T) { fanOutPerUnit(t, fake, c) })
		}
	})
}

func forEachScenario[D, C any](t *testing.T, checks []Check[D, C], fn func(*testing.T, Check[D, C], Scenario)) {
	t.Helper()
	for _, c := range checks {
		t.Run(c.Name, func(t *testing.T) {
			for _, s := range c.Scenarios() {
				t.Run(s.Name, func(t *testing.T) { fn(t, c, s) })
			}
		})
	}
}

func runScenario[D, C any](t *testing.T, b backend[D, C], c Check[D, C], s Scenario) {
	t.Helper()
	base := b.fixture(c)
	first := b.run(t, c, base)
	second := b.run(t, c, s.Edit(t, base))
	s.run(t, b, first, second)
}

// engineOracle is the control for every engine scenario: it proves the shim
// actually records runs and that a repeat of identical work is a cache hit.
// Without it, a backend that silently recorded nothing would make every
// isolation assertion pass.
func engineOracle[D, C any](t *testing.T, b *engineBackend[D, C], c Check[D, C]) {
	t.Helper()
	files := b.fixture(c)

	first := b.run(t, c, files).units
	if len(first) == 0 {
		t.Fatal("the shim recorded no execs at all; the oracle is not observing anything")
	}
	for unit := range c.WantUnitFiles {
		if first[unit] == "" {
			t.Errorf("no exec recorded for unit %q; got %v", unit, sortedKeys(first))
		}
	}

	// Re-running byte-identical work must change nothing.
	second := b.run(t, c, files).units
	if d := delta(first, second); len(d) != 0 {
		t.Errorf("re-running an unchanged tree re-executed %v; the engine is not caching at all", d)
	}
}

// fanOutPerUnit pins the shape the whole caching argument rests on: one run of
// the check's steps per unit, each with only that unit's own files mounted. If
// this drifts — a single exec over the whole tree, say — no amount of cache
// assertions would mean anything.
func fanOutPerUnit[D, C any](t *testing.T, fake *fakeBackend[D, C], c Check[D, C]) {
	t.Helper()
	execs := fake.run(t, c, c.Fixture("")).execs

	byUnit := map[string][]string{}
	for _, e := range execs {
		if unit := c.UnitOf(e); unit != "" {
			byUnit[unit] = append(byUnit[unit], strings.Join(e.Args, " "))
		}
	}

	if len(byUnit) != len(c.WantUnitFiles) {
		t.Fatalf("want %d units, got %d (%v)\n%s",
			len(c.WantUnitFiles), len(byUnit), sortedKeys(byUnit), formatExecs(execs))
	}

	for unit, wantFiles := range c.WantUnitFiles {
		gotArgv, ok := byUnit[unit]
		if !ok {
			t.Errorf("no execs for unit %q\n%s", unit, formatExecs(execs))
			continue
		}
		wantArgv := c.WantUnitArgv(unit)
		if strings.Join(gotArgv, " | ") != strings.Join(wantArgv, " | ") {
			t.Errorf("unit %q runs the wrong steps\nwant: %v\ngot:  %v", unit, wantArgv, gotArgv)
		}
		if got := unitFiles(execs, c, unit); strings.Join(got, ",") != strings.Join(wantFiles, ",") {
			t.Errorf("unit %q has the wrong files mounted\nwant: %v\ngot:  %v",
				unit, wantFiles, got)
		}
	}
}

// unitFiles returns the files mounted at the check's mount for a unit's first exec.
func unitFiles[D, C any](execs []daggerfake.Exec, c Check[D, C], unit string) []string {
	for _, e := range execs {
		if c.UnitOf(e) == unit {
			mount := c.Mount
			if mount == "" {
				mount = strings.TrimPrefix(e.Workdir, "/")
			}
			return e.Files[mount]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// delta returns the entries whose identity changed between two reads.
func delta(before, after units) []string {
	var changed []string
	for k, v := range after {
		if before[k] != v {
			changed = append(changed, fmt.Sprintf("%s %s->%s", k, before[k], v))
		}
	}
	return changed
}

// FormatExecs renders an exec log for a failure message.
func FormatExecs(execs []daggerfake.Exec) string { return formatExecs(execs) }

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
