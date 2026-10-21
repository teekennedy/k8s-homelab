package main

import (
	"maps"
	"slices"
	"testing"

	"dagger/homelab/internal/cachetest"
	"dagger/homelab/internal/dagger"

	// Sets DAGGER_SESSION_PORT/TOKEN before internal/dagger's init() reads
	// them. Without it this test binary panics at startup. See the package doc.
	_ "dagger/homelab/internal/cacheprobe"

	"github.com/Khan/genqlient/graphql"
	"github.com/dagger/querybuilder"
)

// client is this module's side of internal/cachetest: the few calls the
// harness makes through the module's own generated SDK.
type client struct{}

var _ cachetest.Client[*dagger.Directory, *dagger.Container] = client{}

func (client) UseFake(t *testing.T, engine graphql.Client) {
	t.Helper()
	previous := dag
	dag = &dagger.Client{
		Query: (&dagger.Query{}).WithGraphQLQuery(querybuilder.Query().Client(engine)),
	}
	t.Cleanup(func() { dag = previous })
}

func (client) Toolchain() *dagger.Container {
	return dag.Container().From("devenv-toolchain")
}

func (client) Directory(files cachetest.Repo) *dagger.Directory {
	d := dag.Directory()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		d = d.WithNewFile(p, files[p])
	}
	return d
}
