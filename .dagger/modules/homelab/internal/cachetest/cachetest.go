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
// Run drives every scenario through the fake, plus a shape test. The fake
// answers "would this unit re-run" from daggerfake's modelled cache keys rather
// than by running anything, so the whole table runs in milliseconds under a
// plain `go test`. The toolchain a check runs in is a constructor argument, and
// the harness passes a stand-in for it.
//
// That the fake's answers are the engine's is daggerfake's own concern: its
// conformance tests put the same queries to both.
package cachetest

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"dagger/homelab/internal/daggerfake"

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
	// Directory builds files in-process with WithNewFile, which the fake
	// treats as a directory loaded off a filesystem.
	Directory(files Repo) D
}

// Check is one module function these tests can drive, together with everything
// needed to read its work back: how to invoke it, and which execs are its
// per-unit work.
type Check[D, C any] struct {
	// Name identifies the check in test names and failure messages.
	Name string

	// Fixture builds the source tree this check runs against.
	Fixture func() Repo

	// Scenarios are the cache-granularity questions to ask of this check.
	Scenarios func() []Scenario

	// Invoke calls the module function, for every item of a collection.
	// container is the stand-in toolchain to run in.
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
	// execs is the raw exec log, for failure messages and the shape test.
	execs []daggerfake.Exec
}

func (s Scenario) run(t *testing.T, first, second outcome) {
	t.Helper()
	s.assertRerun(t, first, second)
	s.assertCached(t, first, second)
	s.assertNothingUnasserted(t, first, second)
}

func (s Scenario) assertRerun(t *testing.T, first, second outcome) {
	t.Helper()
	for _, unit := range s.Rerun {
		before, existed := first.units[unit]
		after, ok := second.units[unit]
		switch {
		case !ok:
			t.Errorf("unit %q did not run at all after the edit; units that ran: %v",
				unit, sortedKeys(second.units))
		case existed && before == after:
			t.Errorf("unit %q was served from cache, but the edit should have invalidated it\n%s",
				unit, formatExecs(second.execs))
		}
	}
}

func (s Scenario) assertCached(t *testing.T, first, second outcome) {
	t.Helper()
	for _, unit := range s.Cached {
		before, existed := first.units[unit]
		after, ok := second.units[unit]
		switch {
		case !existed:
			t.Errorf("unit %q never ran on the first run; the scenario cannot say it stayed cached",
				unit)
		case !ok:
			t.Errorf("unit %q disappeared after the edit; units that ran: %v",
				unit, sortedKeys(second.units))
		case before != after:
			t.Errorf("unit %q was re-run, but nothing it owns changed; caching is not per-unit\n"+
				"before: %s\nafter:  %s", unit, before, after)
		}
	}
}

// assertNothingUnasserted makes every unit the check produced appear in exactly
// one of Rerun and Cached. Without it a scenario could pass by staying silent
// about the unit that actually broke — including one a fixture grew later.
func (s Scenario) assertNothingUnasserted(t *testing.T, first, second outcome) {
	t.Helper()

	asserted := map[string]bool{}
	for _, u := range append(append([]string(nil), s.Rerun...), s.Cached...) {
		asserted[u] = true
	}
	for _, seen := range []units{first.units, second.units} {
		for unit := range seen {
			if !asserted[unit] {
				t.Errorf("unit %q ran but the scenario says nothing about it; "+
					"add it to Rerun or Cached", unit)
				asserted[unit] = true
			}
		}
	}
}

// ---------------------------------------------------------------------------
// the tests
// ---------------------------------------------------------------------------

// Run is the whole suite for a set of checks: every scenario, and the per-unit
// shape.
func Run[D, C any](t *testing.T, client Client[D, C], checks ...Check[D, C]) {
	t.Helper()
	fake := newFakeBackend(client)

	t.Run("CacheGranularity", func(t *testing.T) {
		forEachScenario(t, checks, func(t *testing.T, c Check[D, C], s Scenario) {
			runScenario(t, fake, c, s)
		})
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

func runScenario[D, C any](t *testing.T, fake *fakeBackend[D, C], c Check[D, C], s Scenario) {
	t.Helper()
	base := c.Fixture()
	first := fake.run(t, c, base)
	second := fake.run(t, c, s.Edit(t, base))
	s.run(t, first, second)
}

// fanOutPerUnit pins the shape the whole caching argument rests on: one run of
// the check's steps per unit, each with only that unit's own files mounted. If
// this drifts — a single exec over the whole tree, say — no amount of cache
// assertions would mean anything.
func fanOutPerUnit[D, C any](t *testing.T, fake *fakeBackend[D, C], c Check[D, C]) {
	t.Helper()
	execs := fake.run(t, c, c.Fixture()).execs

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
