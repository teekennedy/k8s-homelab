package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"dagger/helmfile/internal/dagger"
)

const (
	// trivyChecksDir and trivyIgnoreFile are what an audit reads from
	// trivyConfigPath, and trivyFixturesDir what the checks' own test reads.
	trivyChecksDir   = "checks"
	trivyIgnoreFile  = "trivyignore.yaml"
	trivyFixturesDir = "tests"
	// trivyChartDir is where an audit's config holds the chart's ignore file.
	trivyChartDir = "chart"

	// trivyConfigMount is where an audit mounts the repo-wide Trivy config.
	trivyConfigMount = "/trivy"
	// auditManifestsDir holds the render, one file per resource.
	auditManifestsDir = "/manifests"
	// auditIgnoreFile is the merged ignore file an audit runs with.
	auditIgnoreFile = "/trivyignore.yaml"
)

// auditPrepareScript turns a render into what Trivy audits. Its one argument
// is the chart's own ignore file, which need not exist.
//
// Trivy exempts a finding by check and file, and names a file's findings by
// its path, so the render is split into <Kind>/<namespace>/<name>.yaml, or
// <Kind>/<name>.yaml for a resource whose manifest sets no namespace: that is
// the path a chart's ignore file lists and a failure prints. yq unpacks each
// List into its items, which Trivy would otherwise skip, and prints every
// resource after a comment naming its path; gawk then writes them out,
// appending so that resources that share a path land in one file.
// CustomResourceDefinitions are dropped: no check reads them, and they are most
// of a large render.
//
// The chart's ignore file is then merged into the repo-wide one. Trivy takes a
// single file, and each has the same top-level lists.
const auditPrepareScript = `set -eu
yq -r '((select(.kind == "List") | .items[]), select(.kind != "List")) |
  select(.kind and .kind != "CustomResourceDefinition") |
  "# manifest: " + .kind + "/" + ((.metadata.namespace | select(. != null and . != "") | . + "/") // "") +
    (.metadata.name // .metadata.generateName // "unnamed") + "\n" + to_yaml
' /rendered.yaml > /manifests.yaml
mkdir ` + auditManifestsDir + `
gawk -v root=` + auditManifestsDir + ` '
  /^---$/ { next }
  sub(/^# manifest: /, "") {
    file = root "/" $0 ".yaml"
    dir = file
    sub(/\/[^\/]*$/, "", dir)
    if (!(dir in made)) { system("mkdir -p \"" dir "\""); made[dir] = 1 }
    print "---" >> file
    next
  }
  { print >> file }
' /manifests.yaml
ignores=` + trivyConfigMount + "/" + trivyIgnoreFile + `
if [ -f "$1" ]; then ignores="$ignores $1"; fi
yq eval-all 'explode(.) | . as $file ireduce ({}; . *+ $file)' $ignores > ` + auditIgnoreFile + `
`

// trivyConfig returns the files under trivyConfigPath that an audit reads, and
// under trivyChartDir the chart's ignore file if it has one.
func trivyConfig(source *dagger.Directory, chartPath string) *dagger.Directory {
	config := source.Directory(trivyConfigPath)
	return dag.Directory().
		WithDirectory(trivyChecksDir, config.Directory(trivyChecksDir)).
		WithFile(trivyIgnoreFile, config.File(trivyIgnoreFile)).
		WithDirectory(trivyChartDir, source.Directory(chartPath), dagger.DirectoryWithDirectoryOpts{
			Include: []string{trivyIgnoreFile},
		})
}

// trivyArgs returns a Trivy scan of target with the repo's custom checks
// alongside the built-in ones. --skip-check-update, --skip-version-check and
// --disable-telemetry keep it off the network, so the built-in checks are the
// ones the trivy binary embeds.
func trivyArgs(scanners string, args ...string) []string {
	return append([]string{
		"trivy", "fs",
		"--quiet",
		"--skip-check-update",
		"--skip-version-check",
		"--disable-telemetry",
		"--scanners", scanners,
		"--misconfig-scanners", "kubernetes",
		"--config-check", path.Join(trivyConfigMount, trivyChecksDir),
		"--check-namespaces", "user",
	}, args...)
}

// auditArgs is the Trivy invocation that audits a prepared render.
func auditArgs() []string {
	return trivyArgs("misconfig,secret",
		"--ignorefile", auditIgnoreFile,
		"--table-mode", "detailed",
		"--exit-code", "1",
		auditManifestsDir,
	)
}

// Audits the rendered manifests with Trivy.
//
// Trivy's built-in Kubernetes checks, the custom checks under
// k8s/trivy/checks and its secret scanner all run, and a finding of any
// severity fails the audit. k8s/trivy/trivyignore.yaml lists the checks that
// are switched off; if the chart directory contains a trivyignore.yaml, it
// exempts single resources from single checks (for expected RBAC or privilege
// requirements), naming each as <Kind>/<namespace>/<name>.yaml, or
// <Kind>/<name>.yaml if its manifest sets no namespace.
// +check
func (r *Release) Audit(ctx context.Context) error {
	_, err := r.rendered().
		WithMountedDirectory(trivyConfigMount, r.TrivyConfig).
		WithExec([]string{"sh", "-c", auditPrepareScript, "--", path.Join(trivyConfigMount, trivyChartDir, trivyIgnoreFile)}).
		WithExec(auditArgs()).
		Sync(ctx)
	if err != nil {
		return fmt.Errorf("trivy failed for %s (%s): %w", r.Path, r.Environment, withExecOutput(err))
	}
	return nil
}

// Tests the custom Trivy checks against their fixtures.
//
// Each custom check has a directory under k8s/trivy/tests named after its ID,
// holding manifests it must fail (fail*.yaml) and manifests it must pass
// (pass*.yaml). A check with a typo in a field path would otherwise pass every
// release without ever being able to fail one.
// +check
func (m *Helmfile) Test(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!k8s/trivy/**"]
	source *dagger.Directory,
) error {
	config := source.Directory(trivyConfigPath)
	fixtures, err := config.Directory(trivyFixturesDir).Glob(ctx, "**/*.yaml")
	if err != nil {
		return fmt.Errorf("listing fixtures: %w", err)
	}

	ctr := m.Container.
		WithMountedDirectory(trivyConfigMount, config).
		WithWorkdir(path.Join(trivyConfigMount, trivyFixturesDir))
	// Every finding is wanted here, so no ignore file is passed.
	report, err := ctr.
		WithExec(trivyArgs("misconfig", "--format", "json", "--output", "/report.json", ".")).
		File("/report.json").
		Contents(ctx)
	if err != nil {
		return fmt.Errorf("trivy failed on the fixtures: %w", withExecOutput(err))
	}
	checks, err := ctr.
		WithExec([]string{"grep", "-rhE", `^#\s+id:`, path.Join(trivyConfigMount, trivyChecksDir)}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("listing custom checks: %w", withExecOutput(err))
	}

	problems, err := trivyFixtureProblems(customCheckIDs(checks), fixtures, report)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("custom trivy checks disagree with their fixtures:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

var customCheckID = regexp.MustCompile(`(?m)^#\s+id:\s*(\S+)`)

// customCheckIDs returns the IDs in the metadata of the custom checks, given
// the `id:` lines grepped from them.
func customCheckIDs(metadata string) []string {
	var ids []string
	for _, match := range customCheckID.FindAllStringSubmatch(metadata, -1) {
		ids = append(ids, match[1])
	}
	sort.Strings(ids)
	return ids
}

// trivyReport is the part of `trivy --format json` the fixture test reads.
// Only failures are reported.
type trivyReport struct {
	Results []struct {
		Target            string
		Misconfigurations []struct {
			ID string
		}
	}
}

// trivyFixtureProblems reports where the custom checks and their fixtures
// disagree. fixtures are paths of the form <check ID>/<pass|fail>*.yaml, and
// report is Trivy's JSON report of scanning them:
//
//   - a fail fixture must be failed by its directory's check, and a pass
//     fixture must not be (other checks' findings are not looked at);
//   - every check must have at least one fixture of each kind;
//   - every fixture must belong to a check.
func trivyFixtureProblems(checkIDs, fixtures []string, report string) ([]string, error) {
	failed, err := failedFixtures(report)
	if err != nil {
		return nil, err
	}

	var problems []string
	have := map[string]bool{}
	for _, fixture := range fixtures {
		if problem := fixtureProblem(fixture, checkIDs, failed[fixture]); problem != "" {
			problems = append(problems, fixture+": "+problem)
		}
		name := path.Base(fixture)
		have[path.Join(path.Dir(fixture), name[:min(len(name), 4)])] = true
	}
	for _, id := range checkIDs {
		for _, kind := range []string{"fail", "pass"} {
			if !have[path.Join(id, kind)] {
				problems = append(problems, fmt.Sprintf("%s: no %s*.yaml fixture under %s", id, kind, path.Join(trivyConfigPath, trivyFixturesDir, id)))
			}
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// fixtureProblem says what is wrong with one fixture, given whether its check
// failed it, or returns "".
func fixtureProblem(fixture string, checkIDs []string, failed bool) string {
	id, name := path.Dir(fixture), path.Base(fixture)
	wantFail := strings.HasPrefix(name, "fail")
	switch {
	case !slices.Contains(checkIDs, id):
		return fmt.Sprintf("no custom check has the ID %q", id)
	case !wantFail && !strings.HasPrefix(name, "pass"):
		return "a fixture is named pass*.yaml or fail*.yaml"
	case wantFail && !failed:
		return id + " should fail it, and does not"
	case !wantFail && failed:
		return id + " should pass it, and does not"
	}
	return ""
}

// failedFixtures returns the fixtures in Trivy's JSON report that the check
// they are filed under failed.
func failedFixtures(report string) (map[string]bool, error) {
	var parsed trivyReport
	if err := json.Unmarshal([]byte(report), &parsed); err != nil {
		return nil, fmt.Errorf("parsing trivy report: %w", err)
	}
	failed := map[string]bool{}
	for _, result := range parsed.Results {
		for _, finding := range result.Misconfigurations {
			if path.Dir(result.Target) == finding.ID {
				failed[result.Target] = true
			}
		}
	}
	return failed, nil
}
