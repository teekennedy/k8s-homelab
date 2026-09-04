package main

import (
	"context"
	"strings"
	"testing"

	"dagger/homelab/internal/dagger"
	"dagger/homelab/internal/daggerfake"

	"github.com/dagger/querybuilder"
)

// fakeBackend is the engine-free half of the cache-granularity harness. It
// answers "would this unit re-run" from internal/daggerfake's modelled cache
// keys rather than by running anything, which is what lets the whole scenario
// table run in milliseconds under a plain `go test`. See cache_test.go for the
// harness it plugs into, and the daggerfake package doc for the model's limits.

// fakeBackend answers from internal/daggerfake's modelled cache keys.
type fakeBackend struct{}

func newFakeBackend() fakeBackend { return fakeBackend{} }

func (fakeBackend) name() string          { return "fake" }
func (fakeBackend) fixture(c check) repo  { return c.fixture("") }
func (fakeBackend) buildsToolchain() bool { return true }

// fakeEngine swaps the module's `dag` client for one backed by a fresh fake
// engine, restoring the original when the test ends.
//
// `dag` is package-level state, so tests using this must not call t.Parallel().
func fakeEngine(t *testing.T) *daggerfake.Engine {
	t.Helper()
	engine := daggerfake.New()
	previous := dag
	dag = &dagger.Client{
		Query: (&dagger.Query{}).WithGraphQLQuery(querybuilder.Query().Client(engine)),
	}
	t.Cleanup(func() { dag = previous })
	return engine
}

func (fakeBackend) run(t *testing.T, c check, files repo) outcome {
	t.Helper()
	engine := fakeEngine(t)
	if err := c.invoke(context.Background(), homelab(files), files.directory(), nil); err != nil {
		t.Fatalf("%s against the fake: %v", c.name, err)
	}
	execs := engine.Execs()

	// A unit's identity is its execs' cache keys, in order. Keys fold in
	// everything before them in the chain, so for a multi-step unit the last
	// key would do — but keeping them all means a failure message says which
	// step diverged.
	byUnit := map[string][]string{}
	for _, e := range execs {
		if unit := c.unitOf(e); unit != "" {
			byUnit[unit] = append(byUnit[unit], e.CacheKey)
		}
	}
	out := units{}
	for unit, keys := range byUnit {
		out[unit] = strings.Join(keys, " ")
	}

	return outcome{
		units:     out,
		toolchain: strings.Join(sortedKeys(cacheKeys(toolchainExecs(execs))), " "),
		execs:     execs,
	}
}

// toolchainExecs returns the execs that build the devenv toolchain container.
// Every check in this module runs inside that container, so anything that
// invalidates these invalidates the entire pipeline.
func toolchainExecs(execs []daggerfake.Exec) []daggerfake.Exec {
	var out []daggerfake.Exec
	for _, e := range execs {
		if len(e.Args) > 0 && e.Args[0] == "devenv" {
			out = append(out, e)
		}
	}
	return out
}

// cacheKeys collects the distinct cache keys of a set of execs.
func cacheKeys(execs []daggerfake.Exec) map[string]bool {
	out := map[string]bool{}
	for _, e := range execs {
		out[e.CacheKey] = true
	}
	return out
}
