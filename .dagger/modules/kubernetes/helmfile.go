package main

import (
	"context"
	"errors"
	"fmt"

	"dagger/kubernetes/internal/dagger"
	"dagger/lib/helmfile"
)

// The parts of the helmfile toolchain the workflow plans and deploys with. The
// parsing is shared through dagger/lib/helmfile; these are the thin Dagger
// wrappers around it, which cannot be shared because each module generates its
// own SDK.

const (
	helmfilePath     = helmfile.StatePath
	sharedChartsPath = helmfile.SharedChartsPath
	// helmRepoRoot is where the scoped repo layout is mounted.
	helmRepoRoot = "/repo"
)

// helmfileContainer mounts a scoped repo layout into the toolchain container.
//
// Helm's repository config and index cache are container-local rather than the
// shared helm cache volumes, as in the helmfile toolchain: a fresh config per
// exec means a repository missing from the state file fails here rather than
// being papered over by one an earlier run left registered.
func helmfileContainer(toolchain *dagger.Container, source *dagger.Directory) *dagger.Container {
	return toolchain.
		WithEnvVariable("HELM_CONFIG_HOME", "/helmfile/helm/config").
		WithEnvVariable("HELM_REPOSITORY_CACHE", "/helmfile/helm/repository").
		WithMountedDirectory(helmRepoRoot, source).
		WithWorkdir(helmRepoRoot)
}

// helmfileStateSource is the two files helmfile reads to resolve an
// environment's releases, and nothing else, so an exec over it only re-runs
// when one of them changes.
func helmfileStateSource(source *dagger.Directory, env string) *dagger.Directory {
	return dag.Directory().
		WithFile(helmfilePath, source.File(helmfilePath)).
		WithFile(helmfile.EnvValuesPath(env), source.File(helmfile.EnvValuesPath(env)))
}

// helmfileReleases returns an environment's releases as helmfile resolves them.
func helmfileReleases(
	ctx context.Context,
	source *dagger.Directory,
	env string,
	toolchain *dagger.Container,
) ([]helmfile.Release, error) {
	listJSON, err := helmfileContainer(toolchain, helmfileStateSource(source, env)).
		WithExec([]string{"helmfile", "--environment", env, "build"}).
		WithExec([]string{"helmfile", "--environment", env, "list", "--output", "json"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", env, withExecOutput(err))
	}
	releases, err := helmfile.ParseList(listJSON)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", env, err)
	}
	return releases, nil
}

// helmfileEnvApps returns an environment's generated app list; see
// helmfile.ParseEnvApps.
func helmfileEnvApps(ctx context.Context, source *dagger.Directory, env string) (map[string]map[string]bool, error) {
	valuesPath := helmfile.EnvValuesPath(env)
	envJSON, err := source.File(valuesPath).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", env, valuesPath, err)
	}
	apps, err := helmfile.ParseEnvApps(envJSON)
	if err != nil {
		return nil, fmt.Errorf("%s: parsing %s: %w", env, valuesPath, err)
	}
	return apps, nil
}

// withExecOutput appends a failed exec's output to its error, which otherwise
// only carries the exit code.
func withExecOutput(err error) error {
	if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
		return fmt.Errorf("%w\n%s%s", err, execErr.Stdout, execErr.Stderr)
	}
	return err
}
