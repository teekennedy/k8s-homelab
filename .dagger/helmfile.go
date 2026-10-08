package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// helmfilePath is the repo-relative helmfile state file.
const helmfilePath = "helmfile.yaml.gotmpl"

// defaultHelmfileEnvironments is used when a check is called without
// environments (the +default on each check only applies to CLI calls).
var defaultHelmfileEnvironments = []string{"production"}

// helmfileEnvValuesPath is the generated values file helmfile loads for env.
func helmfileEnvValuesPath(env string) string {
	return path.Join("config/gen", env, "env.json")
}

// helmfileRelease is one release of the helmfile in one environment.
//
// Source holds only what helmfile reads to render that release: the state file,
// the environment's values, the release's chart and the shared library charts.
// That scoping is what makes the result cache per chart: a change to another
// chart, or to another environment's values, leaves this release's execs
// untouched.
type helmfileRelease struct {
	// Name is the release name. The helmfile templates derive a release's chart
	// path from its name, so this is always the chart directory's basename.
	Name string
	// Path is the chart's directory relative to the repo root.
	Path string
	// Environment is the helmfile environment to render with.
	Environment string
	// Source is the scoped repo layout described above.
	Source *dagger.Directory
}

func newHelmfileRelease(source *dagger.Directory, chartPath, env string) *helmfileRelease {
	envValues := helmfileEnvValuesPath(env)
	return &helmfileRelease{
		Name:        filepath.Base(chartPath),
		Path:        chartPath,
		Environment: env,
		Source: dag.Directory().
			WithFile(helmfilePath, source.File(helmfilePath)).
			WithFile(envValues, source.File(envValues)).
			WithDirectory(chartPath, source.Directory(chartPath)).
			WithDirectory(sharedChartsPath, source.Directory(sharedChartsPath)),
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
func (r *helmfileRelease) args(command ...string) []string {
	return append([]string{
		"helmfile",
		"--environment", r.Environment,
		"--selector", "name=" + r.Name,
	}, command...)
}

// rendered runs `helmfile template` for this release, leaving the manifests in
// /rendered.yaml and the chart's dependencies built under its charts/ dir.
//
// This is the only exec for a release that touches the network. Template and
// Validate both start from it, so a release's repositories and dependencies are
// fetched once however many checks consume them. A chart directory with no
// matching release fails here: helmfile exits non-zero when the selector
// matches nothing.
func (r *helmfileRelease) rendered(toolchain *dagger.Container) *dagger.Container {
	// Redirected in a shell rather than with RedirectStdout, which still echoes
	// every rendered manifest into the check's log.
	redirect := []string{"sh", "-c", `exec "$@" > /rendered.yaml`, "--"}
	return helmfileContainer(toolchain, r.Source).
		WithExec(append(redirect, r.args("template", "--include-crds")...))
}

// Template runs `helmfile template` on this release to verify it renders.
func (r *helmfileRelease) Template(ctx context.Context, toolchain *dagger.Container) error {
	if _, err := r.rendered(toolchain).Sync(ctx); err != nil {
		return fmt.Errorf("helmfile template failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// Validate runs `helmfile lint` (helm lint with the release's merged values)
// on this release, reusing the dependencies the render already built.
func (r *helmfileRelease) Validate(ctx context.Context, toolchain *dagger.Container) error {
	_, err := r.rendered(toolchain).
		WithExec(r.args("lint", "--skip-deps")).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("helmfile lint failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// withExecOutput appends a failed exec's output to its error, which otherwise
// only carries the exit code.
func withExecOutput(err error) error {
	if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
		return fmt.Errorf("%w\n%s%s", err, execErr.Stdout, execErr.Stderr)
	}
	return err
}

// matchHelmfilePaths returns the chart paths affected by the given file paths.
// The state file, the environment config and the shared library charts feed
// every release, so a change to any of them selects all charts.
func matchHelmfilePaths(filePaths, chartPaths []string) []string {
	for _, p := range filePaths {
		if p == helmfilePath || strings.HasPrefix(p, "config/") || strings.HasPrefix(p, sharedChartsPath+"/") {
			return chartPaths
		}
	}
	return matchChartPaths(filePaths, chartPaths)
}

// forEachHelmfileRelease runs fn for every chart in every environment and
// returns how many releases it covered. action names what fn does, for the
// error returned when any of them fail.
func (m *Homelab) forEachHelmfileRelease(
	ctx context.Context,
	source *dagger.Directory,
	environments []string,
	paths []string,
	container *dagger.Container,
	action string,
	fn func(context.Context, *helmfileRelease, *dagger.Container) error,
) (int, error) {
	chartPaths := discoverHelmChartPaths(ctx, source)
	if len(paths) > 0 {
		chartPaths = matchHelmfilePaths(paths, chartPaths)
	}
	if len(chartPaths) == 0 {
		return 0, nil
	}
	if len(environments) == 0 {
		environments = defaultHelmfileEnvironments
	}
	if container == nil {
		container = m.ciContainer()
	}

	g := new(errgroup.Group)
	for _, env := range environments {
		for _, chartPath := range chartPaths {
			release := newHelmfileRelease(source, chartPath, env)
			g.Go(func() error {
				return fn(ctx, release, container)
			})
		}
	}
	if err := g.Wait(); err != nil {
		return 0, fmt.Errorf("%s failed: %w", action, err)
	}
	return len(environments) * len(chartPaths), nil
}

// BuildHelmfile renders every chart with `helmfile template`, once per
// environment. Each release is rendered from a scoped source directory so that
// a change to one chart doesn't invalidate the cache for the others.
// When paths are provided, only charts matching the paths are rendered.
// +check
func (m *Homelab) BuildHelmfile(ctx context.Context,
	// +defaultPath="/"
	// charts/*.tgz are built dependencies; they are gitignored, so leaving them
	// out makes a local run resolve dependencies the way a clean checkout does.
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// Helmfile environments to render with.
	// +optional
	// +default=["production"]
	environments []string,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	n, err := m.forEachHelmfileRelease(ctx, source, environments, paths, container, "helmfile template rendering",
		func(ctx context.Context, r *helmfileRelease, c *dagger.Container) error {
			return r.Template(ctx, c)
		})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "Helmfile template rendering skipped (no matching charts)", nil
	}
	return fmt.Sprintf("Helmfile template rendering passed (%d releases)", n), nil
}

// ValidateHelmfile runs `helmfile lint` on every chart, once per environment.
// It lints each chart with the values helmfile would deploy it with.
// When paths are provided, only charts matching the paths are linted.
// +check
func (m *Homelab) ValidateHelmfile(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// Helmfile environments to lint with.
	// +optional
	// +default=["production"]
	environments []string,
	// +optional
	paths []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	n, err := m.forEachHelmfileRelease(ctx, source, environments, paths, container, "helmfile validation",
		func(ctx context.Context, r *helmfileRelease, c *dagger.Container) error {
			return r.Validate(ctx, c)
		})
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "Helmfile validation skipped (no matching charts)", nil
	}
	return fmt.Sprintf("Helmfile validation passed (%d releases)", n), nil
}

// LintHelmfile checks the helmfile state itself, once per environment: that it
// renders (`helmfile build`), and that its releases, the chart directories and
// the environment's app list agree with each other.
// +check
func (m *Homelab) LintHelmfile(ctx context.Context,
	// +defaultPath="/"
	// Only Chart.yaml is needed from k8s/, to know which charts exist.
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/Chart.yaml"]
	source *dagger.Directory,
	// Helmfile environments to lint.
	// +optional
	// +default=["production"]
	environments []string,
	// +optional
	container *dagger.Container,
) (string, error) {
	if len(environments) == 0 {
		environments = defaultHelmfileEnvironments
	}
	if container == nil {
		container = m.ciContainer()
	}
	chartPaths := discoverHelmChartPaths(ctx, source)

	g := new(errgroup.Group)
	for _, env := range environments {
		g.Go(func() error {
			return lintHelmfileState(ctx, source, env, chartPaths, container)
		})
	}
	if err := g.Wait(); err != nil {
		return "", fmt.Errorf("helmfile lint failed: %w", err)
	}

	return fmt.Sprintf("Helmfile lint passed (%s)", strings.Join(environments, ", ")), nil
}

// helmfileListEntry is one release in `helmfile list --output json`.
type helmfileListEntry struct {
	Name  string `json:"name"`
	Chart string `json:"chart"`
}

// lintHelmfileState renders the state file for env and cross-checks it.
func lintHelmfileState(
	ctx context.Context,
	source *dagger.Directory,
	env string,
	chartPaths []string,
	toolchain *dagger.Container,
) error {
	envValuesPath := helmfileEnvValuesPath(env)
	envValues := source.File(envValuesPath)

	// The state file and the environment's values are all `build` and `list`
	// read, so this exec only re-runs when one of those two changes.
	state := helmfileContainer(toolchain, dag.Directory().
		WithFile(helmfilePath, source.File(helmfilePath)).
		WithFile(envValuesPath, envValues)).
		WithExec([]string{"helmfile", "--environment", env, "build"})

	listJSON, err := state.
		WithExec([]string{"helmfile", "--environment", env, "list", "--output", "json"}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", env, withExecOutput(err))
	}
	var releases []helmfileListEntry
	if err := json.Unmarshal([]byte(listJSON), &releases); err != nil {
		return fmt.Errorf("%s: parsing helmfile list output: %w", env, err)
	}

	envJSON, err := envValues.Contents(ctx)
	if err != nil {
		return fmt.Errorf("%s: reading %s: %w", env, envValuesPath, err)
	}
	var values struct {
		Apps map[string]map[string]bool `json:"apps"`
	}
	if err := json.Unmarshal([]byte(envJSON), &values); err != nil {
		return fmt.Errorf("%s: parsing %s: %w", env, envValuesPath, err)
	}

	problems := helmfileStateProblems(releases, chartPaths, values.Apps, envValuesPath)
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
	releases []helmfileListEntry,
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
