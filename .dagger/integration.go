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
// pytest suite run against the result.
//
// Nothing here is release-specific. A release joins the workflow by being
// enabled in the ephemeral environment (config/ephemeral.cue) and growing a
// tests/ directory next to its chart; reflector is just the first one to do it.
// The assertions live in that directory, not here.

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
// as running tests.
//
// Plain data only, and the reports as text rather than a Directory: a directory
// produced by execs that used a service and a secret cannot outlive the session
// that made it, and a function result holding one is not cached across sessions.
// The reports are small enough that nothing is lost.
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
// Deliberately not a `+check`: it needs an engine that allows privileged execs,
// and a PR can change both this code and the tests it runs. Invoke it explicitly:
//
//	dagger call test-kubernetes-integration
//	dagger call test-kubernetes-integration --releases=reflector
//
// The result is cached like any function call: the same source and arguments
// return the earlier verdict without starting a cluster. The cluster is created,
// used and destroyed inside this call. Nothing reads,
// writes or merges a kubeconfig outside the engine, and no host context
// changes. See "Kubernetes integration tests" in README.md for prerequisites.
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
// reports, one file per release:
//
//	dagger call kubernetes-integration-reports export --path=./reports
//
// A failing test is not an error here — getting the report out is the point —
// but a cluster that never came up, or a release that never became healthy,
// still is.
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

// scopedIntegrationRun plans the run against the caller's whole source, then
// hands the workflow only what it reads.
//
// The workflow is run through the module's own API so that Dagger caches it on
// its arguments. The caller's `source` has to cover every chart, because which
// ones a run deploys is only known once the ephemeral environment has been read,
// so a change to any chart re-runs the caller. Re-running the caller costs the
// plan; the workflow itself is a cache hit unless something it deploys changed.
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

	// What helmfile reads to sync those releases — the same layout the
	// per-release render checks use — and nothing else. A release's tests live in
	// its chart directory, so they come along.
	scoped := helmfileStateSource(source, ephemeralEnvironment).
		WithDirectory(sharedChartsPath, source.Directory(sharedChartsPath))
	for _, r := range plan {
		scoped = scoped.WithDirectory(r.Chart, source.Directory(r.Chart))
	}
	// Evaluated before it is passed on. An unevaluated directory is identified
	// by the recipe that builds it, which names the caller's whole source; an
	// evaluated one by its content, which is what has to decide the cache hit.
	scoped, err = scoped.Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("scoping the source to %s: %w", strings.Join(releaseNames(plan), ", "), err)
	}

	return dag.Homelab(dagger.HomelabOpts{DevenvSource: m.DevenvSource}).
		RunKubernetesIntegration(scoped, releaseNames(plan), dagger.HomelabRunKubernetesIntegrationOpts{
			RepeatSync: repeatSync,
			Container:  container,
		}), nil
}

// RunKubernetesIntegration is the cached half of the workflow, and what
// TestKubernetesIntegration and KubernetesIntegrationReports call: plan, create,
// deploy, wait, prove, test, destroy. It is exported only so that they can reach
// it through the module's own API, which is what lets Dagger cache it on its own
// arguments; invoke those two instead.
//
// source is the scoped layout scopedIntegrationSource builds — the state file,
// the ephemeral values and the chosen releases' charts — rather than the whole
// repo. That is the point: the caller's `source` covers everything under k8s/, so
// a change to any chart re-runs the caller, but only a change to something this
// run deploys changes this argument.
func (m *Homelab) RunKubernetesIntegration(ctx context.Context,
	source *dagger.Directory,
	releases []string,
	// +optional
	repeatSync bool,
	// +optional
	container *dagger.Container,
) (*KubernetesIntegrationRun, error) {
	return m.runKubernetesIntegration(ctx, source, releases, repeatSync, container)
}

// runKubernetesIntegration is the whole workflow: plan, create, deploy, wait,
// prove, test, destroy.
func (m *Homelab) runKubernetesIntegration(
	ctx context.Context,
	source *dagger.Directory,
	releases []string,
	repeatSync bool,
	container *dagger.Container,
) (run *KubernetesIntegrationRun, err error) {
	if container == nil {
		container = m.integrationContainer()
	}
	// The same scoping the render checks use: helm's repository config is
	// per-exec, and the repo is mounted where helmfile expects to be run from.
	toolchain := helmfileContainer(container, source)

	// Planned before anything is created, so a typo in --releases or a release
	// that was never enabled costs nothing.
	plan, err := integrationPlan(ctx, source, toolchain, releases)
	if err != nil {
		return nil, err
	}

	cluster, err := newK3sCluster(toolchain)
	if err != nil {
		return nil, err
	}
	// Deferred before the cluster exists so that every path out of here — a
	// failed deploy, a failing test, a panic unwinding through — takes it down.
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
// run created. The order matters: each step is only meaningful once the previous
// one holds.
func deployForTesting(
	ctx context.Context,
	cluster *k3sCluster,
	plan []*integrationRelease,
	repeatSync bool,
) error {
	if err := cluster.Create(ctx); err != nil {
		return err
	}
	// A second sync, when asked for, checks helmfile's idempotency against a
	// cluster that already has the releases on it rather than only an empty one.
	syncs := 1
	if repeatSync {
		syncs = 2
	}
	for attempt := 1; attempt <= syncs; attempt++ {
		if err := syncReleases(ctx, cluster, plan, attempt); err != nil {
			return err
		}
	}

	// Namespace-disjoint and read-only, so they run together: serially their
	// timeouts would sum.
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

// integrationPlan works out which releases to deploy and what the workflow needs
// to know about each: the namespace helmfile puts it in, and where its tests are.
//
// The releases come from the ephemeral environment itself when the caller named
// none. Enablement lives in that environment's generated `apps` map, read by the
// state file's installedTemplate, so it is the only thing that decides what a
// sync installs — which makes it the right default and the only workable source
// of truth. `releases` can then narrow that set, because a --selector genuinely
// does narrow a sync even though it cannot turn a release on.
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
		// Discovered the same way every other Python check discovers a project,
		// so its source is scoped by the same rule.
		projects := pythonProjects(ctx, source, []string{path.Join(testsPath, "pyproject.toml")})
		if len(projects) != 1 {
			return nil, fmt.Errorf("release %q has no Kubernetes integration tests: expected a "+
				"Python project at %s (see k8s/foundation/reflector/tests for the shape of one)",
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
// enables, naming the file to edit rather than leaving helmfile to skip it
// silently — a release the app list disables is simply never installed, and the
// tests would then run against a cluster missing the thing they test.
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
//
// There is no separate state file for development clusters: the same
// helmfile.yaml.gotmpl and the same generated environment values that deploy
// production deploy this, which is the only way the workflow tests what the
// cluster actually runs.
func syncReleases(ctx context.Context, cluster *k3sCluster, plan []*integrationRelease, attempt int) error {
	args := []string{
		"helmfile",
		"--environment", ephemeralEnvironment,
		// KUBECONFIG names the file (WithCluster sets it) and this names the
		// context inside it. Between them there is nothing left for an ambient
		// current-context to decide: a kubeconfig that is not the ephemeral
		// cluster's has no context by this name, and helmfile fails rather than
		// deploying somewhere else.
		"--kube-context", cluster.Context(),
	}
	for _, r := range plan {
		args = append(args, "--selector", "name="+r.Name)
	}
	args = append(args, "sync")

	ctr := cluster.WithCluster(cluster.Toolchain)
	if attempt > 1 {
		// Without this the repeat is the identical exec, which Dagger would
		// serve from cache — proving nothing about syncing twice.
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
// The suites run together — each in its own container, and the suites give their
// namespaces random names precisely so they can — rather than making release
// N+1's `uv` resolve wait on release N's verdict. Results are collected by
// index, so the summary's order follows the plan rather than who finished first.
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
			// The install comes first and knows nothing of the cluster, and it is
			// built from the toolchain without the repo mounted in: a mount is
			// part of an exec's cache key, so one carrying the whole source would
			// reinstall on every unrelated edit.
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

	// Anything else is a failure, including a clean exit with nothing run: the
	// workflow asked for this release's `kubernetes` tests, so collecting none
	// means they were never exercised.
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
