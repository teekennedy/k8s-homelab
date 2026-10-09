package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"dagger/homelab/internal/dagger"
)

// The Kubernetes integration workflow: a throwaway k3d cluster, the releases
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
	// kubernetesMarker is the pytest marker those tests carry. Selecting it is
	// the only way they run; see pytestDeselected.
	kubernetesMarker = "kubernetes"
	// deploymentTimeout bounds the wait for a release's Deployments to become
	// available.
	deploymentTimeout = "5m"
)

// defaultIntegrationReleases is used when the workflow is called without
// releases (the +default on the function only applies to CLI calls).
var defaultIntegrationReleases = []string{"reflector"}

// integrationRelease is one release the workflow deploys and tests.
type integrationRelease struct {
	// Name is the helmfile release name.
	Name string
	// Namespace is the namespace helmfile deploys it into, taken from the
	// helmfile state rather than assumed from the name.
	Namespace string
	// TestsPath is the release's test directory, relative to the repo root.
	TestsPath string
	// Tests is that directory, scoped so pytest sees only the project.
	Tests *dagger.Directory
}

// kubernetesIntegrationRun is the outcome of one workflow run that got as far
// as running tests.
type kubernetesIntegrationRun struct {
	// Summary is the per-release verdict, with pytest's output folded in for
	// the ones that failed.
	Summary string
	// Reports holds one JUnit XML report per release.
	Reports *dagger.Directory
	// Failed names the releases whose tests did not all pass. Empty is a pass;
	// an infrastructure failure is an error instead, not an entry here.
	Failed []string
}

// TestKubernetesIntegration deploys releases to a throwaway k3d cluster and runs
// their Kubernetes integration tests against it.
//
// Deliberately not a `+check`: it needs a Docker daemon and some minutes, and
// `dagger check` is meant to stay runnable with neither. Invoke it explicitly:
//
//	dagger call test-kubernetes-integration
//	dagger call test-kubernetes-integration --releases=reflector
//
// The cluster is created, used and destroyed inside this call. Nothing reads,
// writes or merges a kubeconfig outside the engine, and no host context
// changes. See "Kubernetes integration tests" in README.md for prerequisites.
func (m *Homelab) TestKubernetesIntegration(ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// Helmfile releases to deploy and test. Each one must be enabled in the
	// ephemeral environment and have a tests/ directory beside its chart.
	// +optional
	// +default=["reflector"]
	releases []string,
	// An existing Docker daemon to create the cluster in, as tcp://host:port,
	// whose host the engine can route to. Empty runs a daemon inside the
	// engine, which needs one that allows privileged execs.
	// +optional
	dockerHost string,
	// Sync twice before testing, to check that re-syncing a cluster that
	// already has the releases on it succeeds.
	// +optional
	repeatSync bool,
	// +optional
	container *dagger.Container,
) (string, error) {
	run, err := m.runKubernetesIntegration(ctx, source, integrationOptions{
		Releases:   releases,
		DockerHost: dockerHost,
		RepeatSync: repeatSync,
		Container:  container,
	})
	if err != nil {
		return "", err
	}
	if len(run.Failed) > 0 {
		return "", fmt.Errorf("integration tests failed for %s:\n%s",
			strings.Join(run.Failed, ", "), run.Summary)
	}
	return "Kubernetes integration tests passed\n" + run.Summary, nil
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
	// +ignore=["*", "!helmfile.yaml.gotmpl", "!config/gen/*/env.json", "!k8s/**/*", "k8s/**/charts/*.tgz", "k8s/**/.venv/**", "k8s/**/__pycache__/**", "k8s/**/.pytest_cache/**", "k8s/**/mixins/vendor/**"]
	source *dagger.Directory,
	// +optional
	// +default=["reflector"]
	releases []string,
	// +optional
	dockerHost string,
	// +optional
	repeatSync bool,
	// +optional
	container *dagger.Container,
) (*dagger.Directory, error) {
	run, err := m.runKubernetesIntegration(ctx, source, integrationOptions{
		Releases:   releases,
		DockerHost: dockerHost,
		RepeatSync: repeatSync,
		Container:  container,
	})
	if err != nil {
		return nil, err
	}
	return run.Reports, nil
}

// integrationOptions is what one workflow run varies, gathered so that the
// exported functions and the runner don't each carry the same argument list.
type integrationOptions struct {
	Releases   []string
	DockerHost string
	RepeatSync bool
	Container  *dagger.Container
}

// runKubernetesIntegration is the whole workflow: plan, create, deploy, wait,
// prove, test, destroy.
func (m *Homelab) runKubernetesIntegration(
	ctx context.Context,
	source *dagger.Directory,
	opts integrationOptions,
) (run *kubernetesIntegrationRun, err error) {
	releases := opts.Releases
	if len(releases) == 0 {
		releases = defaultIntegrationReleases
	}
	container := opts.Container
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

	cluster, err := newK3dCluster(ctx, toolchain, opts.DockerHost)
	if err != nil {
		return nil, err
	}
	// Deferred before the cluster exists so that every path out of here — a
	// failed deploy, a failing test, a panic unwinding through — takes it down.
	defer func() {
		err = errors.Join(err, cluster.Close(ctx))
	}()

	if err := deployForTesting(ctx, cluster, plan, opts.RepeatSync); err != nil {
		return nil, err
	}
	return runIntegrationTests(ctx, cluster, plan)
}

// deployForTesting creates the cluster, puts the planned releases on it, waits
// for them to be healthy, and proves the kubeconfig still names the cluster this
// run created — in that order, because each step is only meaningful once the
// previous one holds.
func deployForTesting(
	ctx context.Context,
	cluster *k3dCluster,
	plan []*integrationRelease,
	repeatSync bool,
) error {
	if err := cluster.Create(ctx); err != nil {
		return err
	}
	if err := syncReleases(ctx, cluster, plan, 1); err != nil {
		return err
	}
	if repeatSync {
		// A second sync over the same releases: helmfile's idempotency checked
		// against a cluster that already has them, rather than only against an
		// empty one.
		if err := syncReleases(ctx, cluster, plan, 2); err != nil {
			return fmt.Errorf("re-syncing an already-deployed cluster: %w", err)
		}
	}
	for _, r := range plan {
		if err := cluster.WaitForDeployments(ctx, r.Namespace, deploymentTimeout); err != nil {
			return fmt.Errorf("release %s did not become healthy: %w", r.Name, err)
		}
	}
	// Last gate before any test writes to a cluster.
	return cluster.Verify(ctx)
}

// integrationPlan resolves the releases into what the workflow needs to know
// about each: where helmfile puts it, and where its tests are.
func integrationPlan(
	ctx context.Context,
	source *dagger.Directory,
	toolchain *dagger.Container,
	releases []string,
) ([]*integrationRelease, error) {
	if err := checkEphemeralEnablement(ctx, source, releases); err != nil {
		return nil, err
	}

	state, err := listHelmfileReleases(ctx, toolchain, ephemeralEnvironment)
	if err != nil {
		return nil, err
	}

	plan := make([]*integrationRelease, 0, len(releases))
	for _, name := range releases {
		entry, ok := state[name]
		if !ok {
			return nil, fmt.Errorf("%s has no release named %q", helmfilePath, name)
		}
		if entry.Namespace == "" {
			return nil, fmt.Errorf("release %q has no namespace in the %s environment", name, ephemeralEnvironment)
		}
		testsPath := path.Join(entry.Chart, kubernetesTestsDir)
		found, err := source.Glob(ctx, path.Join(testsPath, "pyproject.toml"))
		if err != nil {
			return nil, fmt.Errorf("looking for %s's integration tests: %w", name, err)
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("release %q has no Kubernetes integration tests: expected a "+
				"Python project at %s (see k8s/foundation/reflector/tests for the shape of one)",
				name, testsPath)
		}
		plan = append(plan, &integrationRelease{
			Name:      name,
			Namespace: entry.Namespace,
			TestsPath: testsPath,
			Tests:     source.Directory(testsPath),
		})
	}
	return plan, nil
}

// checkEphemeralEnablement fails unless the ephemeral environment enables
// exactly the releases being tested.
//
// Enablement is the environment's `apps` map, by way of the helmfile templates'
// installedTemplate. A --selector can narrow what a sync touches but cannot turn
// a release on, and a release left enabled that nobody asked for would be
// deployed regardless of the selector. Reading the generated values makes both
// of those an error here rather than a surprise in the cluster.
func checkEphemeralEnablement(ctx context.Context, source *dagger.Directory, releases []string) error {
	valuesPath := helmfileEnvValuesPath(ephemeralEnvironment)
	contents, err := source.File(valuesPath).Contents(ctx)
	if err != nil {
		return fmt.Errorf("reading %s: %w", valuesPath, err)
	}
	var values struct {
		Apps map[string]map[string]bool `json:"apps"`
	}
	if err := json.Unmarshal([]byte(contents), &values); err != nil {
		return fmt.Errorf("parsing %s: %w", valuesPath, err)
	}

	var enabled []string
	for _, tier := range values.Apps {
		for name, on := range tier {
			if on {
				enabled = append(enabled, name)
			}
		}
	}
	return enablementProblems(enabled, releases, valuesPath)
}

// enablementProblems reports where an environment's enabled releases and the
// requested ones disagree.
func enablementProblems(enabled, requested []string, valuesPath string) error {
	want := map[string]bool{}
	for _, r := range requested {
		want[r] = true
	}
	have := map[string]bool{}
	for _, r := range enabled {
		have[r] = true
	}

	var problems []string
	for _, r := range enabled {
		if !want[r] {
			problems = append(problems, fmt.Sprintf("%s is enabled in %s but was not asked for, "+
				"and a selector cannot disable it", r, valuesPath))
		}
	}
	for _, r := range requested {
		if !have[r] {
			problems = append(problems, fmt.Sprintf("%s was asked for but is not enabled in %s, "+
				"so helmfile would not install it", r, valuesPath))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("the %s environment does not match the releases under test "+
		"(edit config/%s.cue, then run `dagger call export-cue --auto-apply`):\n  %s",
		ephemeralEnvironment, ephemeralEnvironment, strings.Join(problems, "\n  "))
}

// listHelmfileReleases returns an environment's releases by name, as helmfile
// itself resolves them.
func listHelmfileReleases(
	ctx context.Context,
	toolchain *dagger.Container,
	env string,
) (map[string]helmfileListEntry, error) {
	out, err := toolchain.
		WithExec([]string{"helmfile", "--environment", env, "list", "--output", "json"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the %s environment's releases: %w", env, withExecOutput(err))
	}
	var entries []helmfileListEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("parsing helmfile list output for %s: %w", env, err)
	}
	byName := make(map[string]helmfileListEntry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	return byName, nil
}

// syncReleases deploys the planned releases with the repo's own helmfile.
//
// There is no separate state file for development clusters: the same
// helmfile.yaml.gotmpl and the same generated environment values that deploy
// production deploy this, which is the only way the workflow tests what the
// cluster actually runs.
func syncReleases(ctx context.Context, cluster *k3dCluster, plan []*integrationRelease, attempt int) error {
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
func runIntegrationTests(
	ctx context.Context,
	cluster *k3dCluster,
	plan []*integrationRelease,
) (*kubernetesIntegrationRun, error) {
	run := &kubernetesIntegrationRun{Reports: dag.Directory()}
	tested := cluster.WithCluster(cluster.Toolchain)

	var summary []string
	for _, r := range plan {
		project := &PythonProject{Path: r.TestsPath, Source: r.Tests}
		result, err := project.testKubernetes(ctx, tested)
		if err != nil {
			return nil, err
		}
		run.Reports = run.Reports.WithFile(fmt.Sprintf("junit-%s.xml", r.Name), result.Junit)

		contents, err := result.Junit.Contents(ctx)
		if err != nil {
			return nil, fmt.Errorf("reading the JUnit report for %s: %w", r.Name, err)
		}
		report, err := parseJUnitReport(contents)
		if err != nil {
			return nil, fmt.Errorf("%s: %w\n%s", r.Name, err, indent(result.Output, "  "))
		}

		line, failed := releaseVerdict(r, result, report)
		if failed {
			run.Failed = append(run.Failed, r.Name)
		}
		summary = append(summary, line)
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
			kubernetesMarker, release.TestsPath))
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
