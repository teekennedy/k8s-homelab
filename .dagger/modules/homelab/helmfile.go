// Renders, lints and validates every chart with the repo's helmfile, once per
// environment, and audits the rendered manifests with Polaris and kubeconform.
// Each release renders from a scoped source directory, so a change to one chart
// leaves the others cached.

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"
	"dagger/homelab/internal/helmfile"

	"golang.org/x/sync/errgroup"
)

const (
	helmfilePath     = helmfile.StatePath
	sharedChartsPath = helmfile.SharedChartsPath
	// polarisConfigPath is the repo-wide Polaris config: which checks run, and
	// the custom ones.
	polarisConfigPath = "k8s/polaris.yaml"
)

// Helmfile runs helmfile checks over every chart.
type Helmfile struct {
	// +private
	Container *dagger.Container
	// +private
	EnvironmentNames []string
}

// Environments returns the helmfile environments the checks run in, keyed by
// name.
func (m *Helmfile) Environments(
	ctx context.Context,
	// +defaultPath="/"
	// charts/*.tgz are built dependencies; they are gitignored, so leaving them
	// out makes a local run resolve dependencies the way a clean checkout does.
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
) (*HelmfileEnvironments, error) {
	charts, err := discoverHelmChartPaths(ctx, source)
	if err != nil {
		return nil, err
	}
	return &HelmfileEnvironments{
		Names:     m.EnvironmentNames,
		Charts:    charts,
		Source:    source,
		Container: m.Container,
	}, nil
}

// HelmfileEnvironments are the helmfile environments the checks run in.
// +collection
type HelmfileEnvironments struct {
	// +keys
	Names []string
	// +private
	Charts []string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
}

// Environment returns the helmfile environment called name.
// +get
func (es *HelmfileEnvironments) Environment(name string) *HelmfileEnvironment {
	return &HelmfileEnvironment{Name: name, Charts: es.Charts, Source: es.Source, Container: es.Container}
}

// HelmfileEnvironment is one helmfile environment.
type HelmfileEnvironment struct {
	// Name is the environment's name, as helmfile's --environment takes it.
	Name string
	// +private
	Charts []string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
}

// Lint checks the helmfile state itself in this environment: that it renders
// (`helmfile build`), and that its releases, the chart directories and the
// environment's app list agree with each other.
// +check
func (e *HelmfileEnvironment) Lint(ctx context.Context) error {
	if err := lintHelmfileState(ctx, e.Source, e.Name, e.Charts, e.Container); err != nil {
		return fmt.Errorf("helmfile lint failed: %w", err)
	}
	return nil
}

// Releases returns every chart's release in this environment, keyed by
// release name.
func (e *HelmfileEnvironment) Releases() *HelmfileReleases {
	names := make([]string, len(e.Charts))
	for i, chart := range e.Charts {
		names[i] = filepath.Base(chart)
	}
	return &HelmfileReleases{
		Names:       names,
		Charts:      e.Charts,
		Environment: e.Name,
		Source:      e.Source,
		Container:   e.Container,
	}
}

// HelmfileReleases are one environment's releases.
// +collection
type HelmfileReleases struct {
	// Names are the release names. The helmfile templates derive a release's
	// chart path from its name, so each is its chart directory's basename.
	// +keys
	Names []string
	// +private
	Charts []string
	// +private
	Environment string
	// +private
	Source *dagger.Directory
	// +private
	Container *dagger.Container
}

// Release returns the release called name.
// +get
func (rs *HelmfileReleases) Release(name string) (*HelmfileRelease, error) {
	for _, chart := range rs.Charts {
		if filepath.Base(chart) == name {
			return newRelease(rs.Source, chart, rs.Environment, rs.Container), nil
		}
	}
	return nil, fmt.Errorf("no chart for release %q", name)
}

// HelmfileRelease is one release of the helmfile in one environment.
type HelmfileRelease struct {
	// Name is the release name.
	Name string
	// Path is the chart's directory relative to the repo root.
	Path string
	// Environment is the helmfile environment it renders with.
	Environment string
	// Source holds only what helmfile reads to render this release: the state
	// file, the environment's values, the release's chart and the shared
	// library charts. That scoping is what makes the result cache per chart: a
	// change to another chart, or to another environment's values, leaves this
	// release's execs untouched.
	// +private
	Source *dagger.Directory
	// +private
	PolarisConfig *dagger.File
	// +private
	Container *dagger.Container
}

func newRelease(source *dagger.Directory, chartPath, env string, container *dagger.Container) *HelmfileRelease {
	envValues := helmfile.EnvValuesPath(env)
	return &HelmfileRelease{
		Name:        filepath.Base(chartPath),
		Path:        chartPath,
		Environment: env,
		Source: dag.Directory().
			WithFile(helmfilePath, source.File(helmfilePath)).
			WithFile(envValues, source.File(envValues)).
			WithDirectory(chartPath, source.Directory(chartPath)).
			WithDirectory(sharedChartsPath, source.Directory(sharedChartsPath)),
		PolarisConfig: source.File(polarisConfigPath),
		Container:     container,
	}
}

// helmfileContainer mounts a scoped repo layout into the toolchain container.
//
// Helm's repository config and index cache are pointed at container-local
// paths instead of the shared helm cache volumes. Helmfile registers every
// repository in the state file on each run, and `helm repo add` holds the
// repositories.yaml lock while it downloads the index; with one render per
// release running in parallel, a shared file turns into a queue that outlasts
// helm's lock timeout. A fresh config per exec also means a repository missing
// from the state file fails here rather than being papered over by one that an
// earlier run left registered.
func helmfileContainer(toolchain *dagger.Container, source *dagger.Directory) *dagger.Container {
	return toolchain.
		WithEnvVariable("HELM_CONFIG_HOME", "/helmfile/helm/config").
		WithEnvVariable("HELM_REPOSITORY_CACHE", "/helmfile/helm/repository").
		WithMountedDirectory(helmRepoRoot, source).
		WithWorkdir(helmRepoRoot)
}

// args returns a helmfile invocation scoped to this release and environment.
func (r *HelmfileRelease) args(command ...string) []string {
	return append([]string{
		"helmfile",
		"--quiet",
		"--environment", r.Environment,
		"--selector", "name=" + r.Name,
	}, command...)
}

// rendered runs `helmfile template` for this release, leaving the manifests in
// /rendered.yaml and the chart's dependencies built under its charts/ dir.
//
// This is the only exec for a release that touches the network. Every check on
// a release starts from it, so a release's repositories and dependencies are
// fetched once however many checks consume them. A chart directory with no
// matching release fails here: helmfile exits non-zero when the selector
// matches nothing.
func (r *HelmfileRelease) rendered() *dagger.Container {
	// Redirected in a shell rather than with RedirectStdout, which still echoes
	// every rendered manifest into the check's log.
	redirect := []string{"sh", "-c", `exec "$@" > /rendered.yaml`, "--"}
	return helmfileContainer(r.Container, r.Source).
		WithExec(append(redirect, r.args("template", "--include-crds")...))
}

// Build runs `helmfile template` on this release to verify it renders.
// +check
func (r *HelmfileRelease) Build(ctx context.Context) error {
	if _, err := r.rendered().Sync(ctx); err != nil {
		return fmt.Errorf("helmfile template failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// Validate runs `helmfile lint` (helm lint with the release's merged values)
// on this release, reusing the dependencies the render already built.
// +check
func (r *HelmfileRelease) Validate(ctx context.Context) error {
	_, err := r.rendered().
		WithExec(r.args("lint", "--skip-deps")).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("helmfile lint failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// rerooted diffs a path-scoped directory against its changed version as a
// Changeset with repo-relative paths. Diffing the scoped directories directly
// yields paths relative to the unit ("main.go"), and `--auto-apply` — which
// writes relative to the repo root — would drop the file in the wrong place.
// Grafting each side onto an empty tree at path restores the repo-relative
// paths without widening what the exec had mounted.
func rerooted(path string, before, after *dagger.Directory) *dagger.Changeset {
	return dag.Directory().WithDirectory(path, after).
		Changes(dag.Directory().WithDirectory(path, before))
}

// withExecOutput appends a failed exec's output to its error, which otherwise
// only carries the exit code.
func withExecOutput(err error) error {
	if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
		return fmt.Errorf("%w\n%s%s", err, execErr.Stdout, execErr.Stderr)
	}
	return err
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

// lintHelmfileState renders the state file for env and cross-checks it.
func lintHelmfileState(
	ctx context.Context,
	source *dagger.Directory,
	env string,
	chartPaths []string,
	toolchain *dagger.Container,
) error {
	var (
		releases []helmfile.Release
		apps     map[string]map[string]bool
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		releases, err = helmfileReleases(gctx, source, env, toolchain)
		return err
	})
	g.Go(func() (err error) {
		apps, err = helmfileEnvApps(gctx, source, env)
		return err
	})
	if err := g.Wait(); err != nil {
		return fmt.Errorf("loading helmfile state: %w", err)
	}

	problems := helmfileStateProblems(releases, chartPaths, apps, helmfile.EnvValuesPath(env))
	if len(problems) > 0 {
		return fmt.Errorf("%s:\n  %s", env, strings.Join(problems, "\n  "))
	}
	return nil
}

// helmfileStateProblems reports where the releases, the chart directories and
// an environment's app list disagree:
//
//   - a chart directory with no release is never deployed by helmfile;
//   - a release with no chart directory cannot be rendered;
//   - a release missing from the app list is silently treated as not installed,
//     because the state file defaults a missing key to false.
//
// apps is keyed by tier, then release name.
func helmfileStateProblems(
	releases []helmfile.Release,
	chartPaths []string,
	apps map[string]map[string]bool,
	envValuesPath string,
) []string {
	charts := map[string]bool{}
	for _, p := range chartPaths {
		charts[p] = true
	}

	var problems []string
	released := map[string]bool{}
	for _, r := range releases {
		released[r.Chart] = true
		if !charts[r.Chart] {
			problems = append(problems, fmt.Sprintf("release %q points at %s, which is not a chart", r.Name, r.Chart))
			continue
		}
		tier := filepath.Base(filepath.Dir(r.Chart))
		if _, listed := apps[tier][r.Name]; !listed {
			problems = append(problems, fmt.Sprintf("release %q is not listed under apps.%s in %s", r.Name, tier, envValuesPath))
		}
	}
	for _, p := range chartPaths {
		if !released[p] {
			problems = append(problems, fmt.Sprintf("chart %s has no release in %s", p, helmfilePath))
		}
	}

	sort.Strings(problems)
	return problems
}
