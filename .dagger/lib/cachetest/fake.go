package cachetest

import (
	"context"
	"strings"
	"testing"

	"dagger/lib/daggerfake"
)

const fakeName = "fake"

// fakeBackend answers from daggerfake's modelled cache keys.
type fakeBackend[D, C any] struct {
	client  Client[D, C]
	modules []string
}

func newFakeBackend[D, C any](client Client[D, C], modules []string) *fakeBackend[D, C] {
	return &fakeBackend[D, C]{client: client, modules: modules}
}

func (*fakeBackend[D, C]) name() string               { return fakeName }
func (*fakeBackend[D, C]) fixture(c Check[D, C]) Repo { return c.Fixture("") }

// Fake swaps the module's `dag` for one backed by a fresh fake engine that
// knows the given dependency modules, and returns that engine.
func Fake[D, C any](t *testing.T, client Client[D, C], modules ...string) *daggerfake.Engine {
	t.Helper()
	engine := daggerfake.New()
	for _, m := range modules {
		engine.Module(m)
	}
	client.UseFake(t, engine)
	return engine
}

func (b *fakeBackend[D, C]) run(t *testing.T, c Check[D, C], files Repo) outcome {
	t.Helper()
	engine := Fake(t, b.client, b.modules...)
	var noToolchain C
	if err := c.Invoke(context.Background(), b.client.Directory(files), noToolchain); err != nil {
		t.Fatalf("%s against the fake: %v", c.Name, err)
	}
	execs := engine.Execs()

	// A unit's identity is its execs' cache keys, in order. Keys fold in
	// everything before them in the chain, so for a multi-step unit the last
	// key would do — but keeping them all means a failure message says which
	// step diverged.
	byUnit := map[string][]string{}
	for _, e := range execs {
		if unit := c.UnitOf(e); unit != "" {
			byUnit[unit] = append(byUnit[unit], e.CacheKey)
		}
	}
	out := units{}
	for unit, keys := range byUnit {
		out[unit] = strings.Join(keys, " ")
	}

	calls := map[string]bool{}
	for _, call := range engine.ModuleCalls() {
		calls[call] = true
	}

	return outcome{
		units:     out,
		toolchain: strings.Join(sortedKeys(calls), " "),
		execs:     execs,
	}
}
