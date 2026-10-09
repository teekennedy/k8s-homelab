package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"dagger/homelab/internal/dagger"

	"golang.org/x/sync/errgroup"
)

// The Kubernetes integration workflow: a throwaway k3s cluster, the releases
// under test deployed onto it with the repo's own helmfile, and each release's
// pytest suite run against the result. A release joins by being enabled in the
// ephemeral environment and having a tests/ directory next to its chart.

const (
	// ephemeralEnvironment is the helmfile environment the workflow deploys
	// with. Its generated values are what decide which releases are installed.
	ephemeralEnvironment = "ephemeral"
	// kubernetesTestsDir is where a release's integration tests live, relative
	// to its chart directory.
	kubernetesTestsDir = "tests"
	// workloadTimeout bounds the wait for a release's workloads to roll out.
	workloadTimeout = "5m"
)

// integrationRelease is one release the workflow deploys and tests.
type integrationRelease struct {
	// Name is the helmfile release name.
	Name string
	// Chart is the chart's directory relative to the repo root.
	Chart string
	// Namespace is the namespace helmfile deploys it into, taken from the
	// helmfile state rather than assumed from the name.
	Namespace string
	// Tests is the release's test project, discovered beside its chart.
	Tests *PythonProject
}

// KubernetesIntegrationRun is the outcome of one workflow run that got as far
// as running tests. It is plain data, with no Directory, so that the call is
// cached; see Caching in README.md.
type KubernetesIntegrationRun struct {
	// Summary is the per-release verdict, with pytest's output folded in for
	// the ones that failed.
	Summary string
	// Reports holds one JUnit XML report per release.
	Reports []*KubernetesIntegrationReport
	// Failed names the releases whose tests did not all pass. Empty is a pass;
	// an infrastructure failure is an error instead, not an entry here.
	Failed []string
}

// KubernetesIntegrationReport is one release's JUnit XML report.
type KubernetesIntegrationReport struct {
	// Release is the helmfile release the report is for.
	Release string
	// XML is the report's contents.
	XML string
}

// TestKubernetesIntegration deploys releases to a throwaway k3s cluster and runs
// their Kubernetes integration tests against it.
//
// Deliberately not a `+check`: it needs an engine that allows privileged execs.
// See "Kubernetes integration tests" in README.md.
//
//	dagger call test-kubernetes-integration
//	dagger call test-kubernetes-integration --releases=<name>
func (m *Homelab) TestKubernetesIntegration(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/ephemeral/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// Helmfile releases to deploy and test, narrowing the set the ephemeral
	// environment enables. Empty means all of them.
	// +optional
	releases []string,
	// Sync twice before testing, to check that re-syncing a cluster that
	// already has the releases on it succeeds.
	// +optional
	repeatSync bool,
	// +optional
	container *dagger.Container,
) (string, error) {
	run, err := m.scopedIntegrationRun(ctx, source, releases, repeatSync, container)
	if err != nil {
		return "", err
	}
	summary, err := run.Summary(ctx)
	if err != nil {
		return "", fmt.Errorf("running the Kubernetes integration tests: %w", err)
	}
	failed, err := run.Failed(ctx)
	if err != nil {
		return "", fmt.Errorf("reading which releases failed: %w", err)
	}
	if len(failed) > 0 {
		return "", fmt.Errorf("integration tests failed for %s:\n%s",
			strings.Join(failed, ", "), summary)
	}
	return "Kubernetes integration tests passed\n" + summary, nil
}

// KubernetesIntegrationReports runs the same workflow and returns the JUnit XML
// reports, one file per release. A failing test is not an error here, but a
// cluster that never came up, or a release that never became healthy, is.
//
//	dagger call kubernetes-integration-reports export --path=./reports
func (m *Homelab) KubernetesIntegrationReports(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/ephemeral/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	releases []string,
	// +optional
	repeatSync bool,
	// +optional
	container *dagger.Container,
) (*dagger.Directory, error) {
	run, err := m.scopedIntegrationRun(ctx, source, releases, repeatSync, container)
	if err != nil {
		return nil, err
	}
	reports, err := run.Reports(ctx)
	if err != nil {
		return nil, fmt.Errorf("running the Kubernetes integration tests: %w", err)
	}
	dir := dag.Directory()
	for _, report := range reports {
		release, err := report.Release(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading a report's release name: %w", err)
		}
		xml, err := report.XML(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the report for %s: %w", release, err)
		}
		dir = dir.WithNewFile(fmt.Sprintf("junit-%s.xml", release), xml)
	}
	return dir, nil
}

// scopedIntegrationRun plans against the caller's whole source, then calls
// RunKubernetesIntegration with only what that run reads, so that its result is
// cached on that and not on the rest of the repo. See Caching in README.md.
func (m *Homelab) scopedIntegrationRun(
	ctx context.Context,
	source *dagger.Directory,
	releases []string,
	repeatSync bool,
	container *dagger.Container,
) (*dagger.HomelabKubernetesIntegrationRun, error) {
	toolchain := container
	if toolchain == nil {
		toolchain = m.integrationContainer()
	}
	plan, err := integrationPlan(ctx, source, toolchain, releases)
	if err != nil {
		return nil, err
	}

	// Evaluated before it is passed on: an unevaluated directory is identified by
	// the recipe that builds it, which names the caller's whole source.
	scoped, err := integrationSource(source, plan).Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("scoping the source to %s: %w", strings.Join(releaseNames(plan), ", "), err)
	}

	return dag.Homelab(dagger.HomelabOpts{DevenvSource: m.DevenvSource}).
		RunKubernetesIntegration(scoped, releaseNames(plan), dagger.HomelabRunKubernetesIntegrationOpts{
			RepeatSync: repeatSync,
			Container:  container,
		}), nil
}

// integrationSource is what helmfile and the tests read for the planned
// releases: the state file, the ephemeral values, the shared charts and each
// release's chart directory, tests included.
func integrationSource(source *dagger.Directory, plan []*integrationRelease) *dagger.Directory {
	scoped := helmfileStateSource(source, ephemeralEnvironment).
		WithDirectory(sharedChartsPath, source.Directory(sharedChartsPath))
	for _, r := range plan {
		scoped = scoped.WithDirectory(r.Chart, source.Directory(r.Chart))
	}
	return scoped
}

// RunKubernetesIntegration is the workflow itself: plan, create, deploy, wait,
// prove, test, destroy. It is exported only so that TestKubernetesIntegration
// and KubernetesIntegrationReports can call it through the module's own API;
// invoke those instead.
//
// source is the layout integrationSource builds, not the whole repo.
func (m *Homelab) RunKubernetesIntegration(ctx context.Context,
	source *dagger.Directory,
	releases []string,
	// +optional
	repeatSync bool,
	// +optional
	container *dagger.Container,
) (run *KubernetesIntegrationRun, err error) {
	if container == nil {
		container = m.integrationContainer()
	}
	toolchain := helmfileContainer(container, source)

	// Planned before anything is created, so a typo in --releases costs nothing.
	plan, err := integrationPlan(ctx, source, toolchain, releases)
	if err != nil {
		return nil, err
	}

	cluster, err := newK3sCluster(toolchain)
	if err != nil {
		return nil, err
	}
	// Deferred before the cluster exists so that every path out of here takes it
	// down.
	defer func() {
		err = errors.Join(err, cluster.Close(ctx))
	}()

	if err := deployForTesting(ctx, cluster, plan, repeatSync); err != nil {
		return nil, err
	}
	return runIntegrationTests(ctx, cluster, plan, container)
}

// deployForTesting creates the cluster, puts the planned releases on it, waits
// for them to roll out, and proves the kubeconfig still names the cluster this
// run created.
func deployForTesting(
	ctx context.Context,
	cluster *k3sCluster,
	plan []*integrationRelease,
	repeatSync bool,
) error {
	if err := cluster.Create(ctx); err != nil {
		return err
	}
	// A second sync checks that re-syncing a populated cluster succeeds.
	syncs := 1
	if repeatSync {
		syncs = 2
	}
	for attempt := 1; attempt <= syncs; attempt++ {
		if err := syncReleases(ctx, cluster, plan, attempt); err != nil {
			return err
		}
	}

	// Concurrent, so the timeouts don't sum.
	g := new(errgroup.Group)
	for _, r := range plan {
		g.Go(func() error {
			if err := cluster.WaitForWorkloads(ctx, r.Namespace, workloadTimeout); err != nil {
				return fmt.Errorf("release %s did not become healthy: %w", r.Name, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("waiting for the deployed releases: %w", err)
	}

	// Last gate before any test writes to a cluster.
	return cluster.Verify(ctx)
}

// integrationPlan works out which releases to deploy, the namespace helmfile
// puts each in, and where its tests are.
//
// The default is every release the ephemeral environment enables, since that
// map is what the state file's installedTemplate reads. `releases` can narrow
// that set, but a --selector cannot turn a release on.
func integrationPlan(
	ctx context.Context,
	source *dagger.Directory,
	toolchain *dagger.Container,
	releases []string,
) ([]*integrationRelease, error) {
	apps, err := helmfileEnvApps(ctx, source, ephemeralEnvironment)
	if err != nil {
		return nil, err
	}
	enabled := enabledReleases(apps)
	if len(releases) == 0 {
		releases = enabled
	}
	if err := checkEnabled(enabled, releases); err != nil {
		return nil, err
	}

	entries, err := helmfileReleases(ctx, source, ephemeralEnvironment, toolchain)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]helmfileListEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}

	plan := make([]*integrationRelease, 0, len(releases))
	for _, name := range releases {
		entry, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("%s has no release named %q", helmfilePath, name)
		}
		if entry.Namespace == "" {
			return nil, fmt.Errorf("release %q has no namespace in the %s environment", name, ephemeralEnvironment)
		}
		testsPath := path.Join(entry.Chart, kubernetesTestsDir)
		projects := pythonProjects(ctx, source, []string{path.Join(testsPath, "pyproject.toml")})
		if len(projects) != 1 {
			return nil, fmt.Errorf("release %q has no Kubernetes integration tests: expected a "+
				"Python project at %s ",
				name, testsPath)
		}
		plan = append(plan, &integrationRelease{
			Name:      name,
			Chart:     entry.Chart,
			Namespace: entry.Namespace,
			Tests:     projects[0],
		})
	}
	return plan, nil
}

// enabledReleases returns the release names an environment's app list turns on.
func enabledReleases(apps map[string]map[string]bool) []string {
	var enabled []string
	for _, tier := range apps {
		for name, on := range tier {
			if on {
				enabled = append(enabled, name)
			}
		}
	}
	sort.Strings(enabled)
	return enabled
}

// checkEnabled fails unless every requested release is one the environment
// enables. Helmfile would otherwise skip a disabled release silently and the
// tests would run against a cluster missing it.
func checkEnabled(enabled, requested []string) error {
	on := map[string]bool{}
	for _, r := range enabled {
		on[r] = true
	}
	var missing []string
	for _, r := range requested {
		if !on[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("the %s environment does not enable %s, so helmfile would not install "+
		"them (edit config/%s.cue, then run `dagger call export-cue --auto-apply`); it enables %s",
		ephemeralEnvironment, strings.Join(missing, ", "), ephemeralEnvironment,
		strings.Join(enabled, ", "))
}

// syncReleases deploys the planned releases with the repo's own helmfile.
func syncReleases(ctx context.Context, cluster *k3sCluster, plan []*integrationRelease, attempt int) error {
	args := []string{
		"helmfile",
		"--environment", ephemeralEnvironment,
		// A kubeconfig that is not the ephemeral cluster's has no context by this
		// name, so helmfile fails rather than deploying somewhere else.
		"--kube-context", cluster.Context(),
	}
	for _, r := range plan {
		args = append(args, "--selector", "name="+r.Name)
	}
	args = append(args, "sync")

	ctr := cluster.WithCluster(cluster.Toolchain)
	if attempt > 1 {
		// Otherwise the repeat is the identical exec and comes from cache.
		ctr = ctr.WithEnvVariable("HOMELAB_HELMFILE_SYNC", strconv.Itoa(attempt))
	}

	if _, err := ctr.WithExec(args).Sync(ctx); err != nil {
		return fmt.Errorf("helmfile sync (attempt %d) failed for %s in the %s environment: %w",
			attempt, strings.Join(releaseNames(plan), ", "), ephemeralEnvironment, withExecOutput(err))
	}
	return nil
}

// runIntegrationTests runs each release's pytest suite against the cluster.
//
// The suites run concurrently. Results are collected by index, so the summary
// follows the plan's order.
func runIntegrationTests(
	ctx context.Context,
	cluster *k3sCluster,
	plan []*integrationRelease,
	container *dagger.Container,
) (*KubernetesIntegrationRun, error) {
	results := make([]*pytestRun, len(plan))
	reports := make([]*junitReport, len(plan))
	contents := make([]string, len(plan))

	g := new(errgroup.Group)
	for i, r := range plan {
		g.Go(func() error {
			// Installed before the cluster is applied, so the install is cached.
			env := r.Tests.kubernetesEnv(container)
			result, err := r.Tests.testKubernetes(ctx, cluster.WithCluster(env))
			if err != nil {
				return err
			}
			xml, err := result.Junit.Contents(ctx)
			if err != nil {
				return fmt.Errorf("reading the JUnit report for %s: %w", r.Name, err)
			}
			report, err := parseJUnitReport(xml)
			if err != nil {
				return fmt.Errorf("%s: %w\n%s", r.Name, err, indent(result.Output, "  "))
			}
			results[i], reports[i], contents[i] = result, report, xml
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("running the integration tests: %w", err)
	}

	run := &KubernetesIntegrationRun{}
	summary := make([]string, len(plan))
	for i, r := range plan {
		run.Reports = append(run.Reports, &KubernetesIntegrationReport{Release: r.Name, XML: contents[i]})
		line, failed := releaseVerdict(r, results[i], reports[i])
		if failed {
			run.Failed = append(run.Failed, r.Name)
		}
		summary[i] = line
	}

	run.Summary = strings.Join(summary, "\n")
	return run, nil
}

// releaseVerdict turns one pytest run into the line it contributes to the
// summary, and whether it counts as a failure.
func releaseVerdict(
	release *integrationRelease,
	result *pytestRun,
	report *junitReport,
) (string, bool) {
	totals := report.Totals()

	if result.ExitCode == 0 && totals.Tests > 0 {
		return fmt.Sprintf("%s: %s", release.Name, totals), false
	}

	// A clean exit with nothing collected is a failure too: the tests never ran.
	detail := []string{fmt.Sprintf("%s: %s (pytest exit code %d)", release.Name, totals, result.ExitCode)}
	if totals.Tests == 0 {
		detail = append(detail, fmt.Sprintf("  no tests marked %q were collected in %s",
			kubernetesMarker, release.Tests.Path))
	}
	for _, problem := range report.Problems() {
		detail = append(detail, "  "+problem)
	}
	detail = append(detail, indent(result.Output, "  "))
	return strings.Join(detail, "\n"), true
}

// indent prefixes every line of s, so pytest's log stays distinguishable from
// the summary it is attached to.
func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// releaseNames lists a plan's release names.
func releaseNames(plan []*integrationRelease) []string {
	names := make([]string, len(plan))
	for i, r := range plan {
		names[i] = r.Name
	}
	return names
}
