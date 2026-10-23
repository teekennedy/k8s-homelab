package main

import (
	"context"
	"fmt"

	"dagger/kubernetes/internal/dagger"
	"dagger/lib/helmfile"
)

// The parts of the helmfile module the workflow plans and deploys with. The
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
// Helm's config, index cache and chart cache stay on the toolchain's shared
// cache volumes; see withRepoConfig for how execs share repositories.yaml.
func helmfileContainer(toolchain *dagger.Container, source *dagger.Directory) *dagger.Container {
	return toolchain.
		WithMountedDirectory(helmRepoRoot, source).
		WithWorkdir(helmRepoRoot)
}

// withRepoConfig wraps a helmfile command that registers repositories, running
// it in a shell that points HELM_REPOSITORY_CONFIG at a repositories file named
// after the state file's content. redirect is appended to the command, e.g.
// " > /rendered.yaml".
//
// `helm repo add` holds the repositories file's lock while it downloads the
// index, so parallel renders queue on a shared file unless an already
// registered repository is skipped, which is what --disable-force-update does.
// Keying the file by the state file's content keeps that safe: a repository
// that is renamed, re-pointed or removed starts a fresh file, so it fails or
// conflicts here rather than being papered over by an entry an earlier run
// left behind. `helm dependency build` still refreshes every index, so a
// chart version newer than the cached index resolves.
func withRepoConfig(redirect string, command []string) []string {
	script := `export HELM_REPOSITORY_CONFIG="$HELM_CONFIG_HOME/repositories-$(sha256sum ` +
		helmfilePath + ` | cut -c1-16).yaml"; exec "$@"` + redirect
	return append([]string{"sh", "-c", script, "--"}, command...)
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
