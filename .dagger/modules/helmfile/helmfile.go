// Renders, lints and validates every chart with the repo's helmfile, and
// audits the rendered manifests with Polaris.
// Each release renders from a scoped source directory, so a change to one chart
// leaves the others cached.

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"dagger/helmfile/internal/dagger"
	"dagger/lib/helmfile"

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
	Environment string
}

// Renders, lints, validates and audits every chart with helmfile.
func New(
	// The container to run in. dagger.toml wires in the nix module's
	// ci-toolchain.
	container *dagger.Container,
	// The helmfile environment to render every chart in.
	// +optional
	// +default="production"
	environment string,
) *Helmfile {
	return &Helmfile{Container: container, Environment: environment}
}

// Checks that the releases, charts and app list agree.
//
// It renders the helmfile state (`helmfile list`) in the environment, then
// checks that every chart directory has a release, every release has a chart
// directory, and every release is listed in the environment's app list.
// +check
func (m *Helmfile) Lint(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
) error {
	charts, err := discoverHelmChartPaths(ctx, source)
	if err != nil {
		return err
	}
	if err := lintHelmfileState(ctx, source, m.Environment, charts, m.Container); err != nil {
		return fmt.Errorf("helmfile lint failed: %w", err)
	}
	return nil
}

// Releases returns every chart's release, keyed by release name.
func (m *Helmfile) Releases(
	ctx context.Context,
	// +defaultPath="/"
	// charts/*.tgz are built dependencies; they are gitignored, so leaving them
	// out makes a local run resolve dependencies the way a clean checkout does.
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
) (*Releases, error) {
	charts, err := discoverHelmChartPaths(ctx, source)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(charts))
	for i, chart := range charts {
		names[i] = filepath.Base(chart)
	}
	return &Releases{
		Names:       names,
		Charts:      charts,
		Environment: m.Environment,
		Source:      source,
		Container:   m.Container,
	}, nil
}

// Releases are the helmfile's releases, one per chart.
// +collection
type Releases struct {
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
func (rs *Releases) Release(name string) (*Release, error) {
	for _, chart := range rs.Charts {
		if filepath.Base(chart) == name {
			return newRelease(rs.Source, chart, rs.Environment, rs.Container), nil
		}
	}
	return nil, fmt.Errorf("no chart for release %q", name)
}

// Release is one release of the helmfile.
type Release struct {
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

func newRelease(source *dagger.Directory, chartPath, env string, container *dagger.Container) *Release {
	envValues := helmfile.EnvValuesPath(env)
	return &Release{
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

// args returns a helmfile invocation scoped to this release and environment.
func (r *Release) args(command ...string) []string {
	return append([]string{
		"helmfile",
		"--quiet",
		"--disable-force-update",
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
func (r *Release) rendered() *dagger.Container {
	// Redirected in the shell rather than with RedirectStdout, which still
	// echoes every rendered manifest into the check's log.
	return helmfileContainer(r.Container, r.Source).
		WithExec(withRepoConfig(" > /rendered.yaml", r.args("template", "--include-crds")))
}

// Renders the release with `helmfile template`.
// +check
func (r *Release) Render(ctx context.Context) error {
	if _, err := r.rendered().Sync(ctx); err != nil {
		return fmt.Errorf("helmfile template failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// Returns rendered template output from `helmfile template`.
func (r *Release) Output(ctx context.Context) (*dagger.File, error) {
	ctr, err := r.rendered().Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("helmfile template failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return ctr.File("/rendered.yaml"), nil
}

// Lints the release's chart with `helmfile lint`.
//
// helmfile lint runs helm lint with the release's merged values. It reuses the
// dependencies the render built.
// +check
func (r *Release) Lint(ctx context.Context) error {
	_, err := r.rendered().
		WithExec(r.args("lint", "--skip-deps")).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("helmfile lint failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
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
