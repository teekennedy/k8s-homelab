package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"dagger/kubernetes/internal/dagger"
	"dagger/lib/cachetest"

	// Sets DAGGER_SESSION_PORT/TOKEN before internal/dagger's init() reads
	// them. Without it this test binary panics at startup. See the package doc.
	_ "dagger/lib/cacheprobe"

	"github.com/Khan/genqlient/graphql"
	"github.com/dagger/querybuilder"
)

// client is this module's side of dagger/lib/cachetest: the few calls the
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

func (client) Directory(files cachetest.Repo) *dagger.Directory {
	d := dag.Directory()
	for _, p := range slices.Sorted(maps.Keys(files)) {
		d = d.WithNewFile(p, files[p])
	}
	return d
}

func (client) HostDirectory(ctx context.Context, path string) (*dagger.Directory, error) {
	dir := dag.Address(path).Directory()
	if _, err := dir.Entries(ctx); err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	return dir, nil
}

func (client) Container(image, mount, volume string, files map[string]string) *dagger.Container {
	ctr := dag.Container().From(image).WithMountedCache(mount, dag.CacheVolume(volume))
	for p, c := range files {
		ctr = ctr.WithNewFile(p, c, dagger.ContainerWithNewFileOpts{Permissions: 0o755})
	}
	return ctr
}

func (c client) Stdout(ctx context.Context, image, mount, volume, env, script string) (string, error) {
	out, err := c.Container(image, mount, volume, nil).
		WithEnvVariable("READ", env).
		WithExec([]string{"sh", "-c", script}).
		Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("running %q: %w", script, err)
	}
	return out, nil
}
