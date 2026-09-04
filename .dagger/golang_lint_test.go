package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dagger/homelab/internal/dagger"
)

// The tests in cache_test.go answer "what would re-run"; they never run a
// command. These answer the other half: does LintGo actually do the thing it
// claims — reformat, tidy, and fail on what autofixing cannot resolve.
//
// That needs a real engine and a real toolchain, so they skip without a Dagger
// session, exactly like engine_cache_test.go:
//
//	cd .dagger && dagger run go test ./...
//
// or via the VerifyCacheGranularity function.
//
// # Why not the devenv container
//
// The faithful toolchain is ciContainer(), but building it costs a Nix build,
// and nothing asserted here depends on devenv: every assertion is about
// golangci-lint reading the repo's real .golangci.yaml. So these run in
// golangci-lint's own image, which bundles a Go toolchain.
//
// The version below and the one devenv.lock pins will drift apart. That is
// tolerable because the assertions are about behaviour that is stable across
// golangci-lint releases — gofumpt reformats, errcheck reports an unchecked
// error — rather than about a particular version's output. What would not be
// tolerable is asserting on exact diagnostic wording, so don't.
//
// renovate: datasource=docker depName=golangci/golangci-lint
const lintImage = "golangci/golangci-lint:v2.13.2"

// lintModPath is where the fixture module lives. It is deliberately nested:
// LintGo scopes each module to its own mount, so the changeset it returns has
// to be re-rooted back to repo-relative paths before `--auto-apply` can use it,
// and a module at the tree root would not notice if that stopped happening.
const lintModPath = "k8s/apps/probe/files/tool"

// cleanGoMod is a go.mod `go mod tidy` leaves untouched, so a case that is not
// about tidying does not pick up an incidental go.mod edit.
const cleanGoMod = "module example.com/probe\n\ngo 1.26\n"

// cleanMain is a main.go that both gofumpt and the enabled linters accept.
const cleanMain = "package main\n\nfunc main() {}\n"

// lintFixture builds a one-module tree carrying the repo's real .golangci.yaml,
// so these tests exercise the config the gate actually runs with rather than a
// stand-in that could pass while the real one is broken.
func lintFixture(config string, module map[string]string) repo {
	files := repo{".golangci.yaml": config}
	for p, c := range module {
		files[filepath.Join(lintModPath, p)] = c
	}
	return files
}

// realGolangciConfig reads the repo's own golangci-lint config, once per test
// run rather than once per case.
func realGolangciConfig(t *testing.T) string {
	t.Helper()
	// The tests run with .dagger as the working directory; see plant().
	config, err := os.ReadFile(filepath.Join("..", ".golangci.yaml"))
	if err != nil {
		t.Fatalf("reading the repo's .golangci.yaml: %v\n"+
			"these tests must run from the .dagger directory", err)
	}
	return string(config)
}

// lintToolchain is the container LintGo runs in for these tests.
func lintToolchain() *dagger.Container {
	return dag.Container().From(lintImage)
}

type lintBehaviour struct {
	// name identifies the case; it becomes the subtest name.
	name string

	// module is the fixture module's files, relative to lintModPath.
	module map[string]string

	// wantErr, when set, is a substring LintGo's error must contain. A case
	// that sets it asserts LintGo fails and returns no changeset.
	wantErr string

	// wantModified are the repo-relative paths the changeset must modify.
	// Empty means the changeset must be empty.
	wantModified []string

	// wantAfter maps a path under lintModPath to the exact contents LintGo's
	// fixes must produce.
	wantAfter map[string]string
}

func lintBehaviours() []lintBehaviour {
	return []lintBehaviour{
		{
			name: "reformats a badly formatted file",
			module: map[string]string{
				"go.mod":  cleanGoMod,
				"main.go": "package main\n\nimport \"fmt\"\n\nfunc main()  {\n\tx :=  1\n\tfmt.Println( x )\n}\n",
			},
			wantModified: []string{lintModPath + "/main.go"},
			wantAfter: map[string]string{
				"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tx := 1\n\tfmt.Println(x)\n}\n",
			},
		},
		{
			// `require ()` is an unused block: tidy drops it without needing to
			// resolve anything, so this case stays offline. It also picks up
			// tidy's canonical blank line after the module directive.
			name: "tidies go.mod",
			module: map[string]string{
				"go.mod":  "module example.com/probe\ngo 1.26\nrequire ()\n",
				"main.go": cleanMain,
			},
			wantModified: []string{lintModPath + "/go.mod"},
			wantAfter:    map[string]string{"go.mod": cleanGoMod},
		},
		{
			// errcheck has no autofix, so --fix leaves the issue behind and the
			// exec exits non-zero. That is the gate: LintGo has to surface it
			// rather than returning a changeset that looks like success.
			name: "fails on an issue --fix cannot resolve",
			module: map[string]string{
				"go.mod":  cleanGoMod,
				"main.go": "package main\n\nimport \"os\"\n\nfunc main() {\n\tos.Setenv(\"A\", \"B\")\n}\n",
			},
			wantErr: "errcheck",
		},
		{
			// The control: without it, a LintGo that reported every file as
			// modified would still pass the two cases above.
			name: "leaves a clean module alone",
			module: map[string]string{
				"go.mod":  cleanGoMod,
				"main.go": cleanMain,
			},
		},
	}
}

func TestLintGoBehaviour(t *testing.T) {
	requireEngine(t)
	config := realGolangciConfig(t)

	for _, c := range lintBehaviours() {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			files := lintFixture(config, c.module)

			// A bare receiver on purpose: with a container supplied, LintGo
			// never touches DevenvSource, and leaving it nil proves that.
			changes, err := (&Homelab{}).LintGo(ctx, files.directory(), lintToolchain())

			if c.wantErr != "" {
				switch {
				case err == nil:
					t.Fatalf("want an error mentioning %q, got none", c.wantErr)
				case !strings.Contains(err.Error(), c.wantErr):
					t.Fatalf("error should mention %q, got: %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LintGo: %v", err)
			}

			assertModified(t, ctx, changes, c.wantModified)
			assertContents(t, ctx, changes, c.wantAfter)
		})
	}
}

func assertModified(t *testing.T, ctx context.Context, changes *dagger.Changeset, want []string) {
	t.Helper()

	empty, err := changes.IsEmpty(ctx)
	if err != nil {
		t.Fatalf("Changeset.IsEmpty: %v", err)
	}
	if len(want) == 0 {
		if !empty {
			modified, _ := changes.ModifiedPaths(ctx)
			t.Errorf("want no changes, got modifications to %v", modified)
		}
		return
	}
	if empty {
		t.Fatalf("want modifications to %v, got an empty changeset", want)
	}

	got, err := changes.ModifiedPaths(ctx)
	if err != nil {
		t.Fatalf("Changeset.ModifiedPaths: %v", err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("wrong files modified\nwant: %v\ngot:  %v\n"+
			"paths must be repo-relative; module-relative ones mean GoModule.Lint "+
			"stopped re-rooting its changeset and --auto-apply would write to the "+
			"wrong place", want, got)
	}
}

func assertContents(t *testing.T, ctx context.Context, changes *dagger.Changeset, want map[string]string) {
	t.Helper()
	for name, wantBody := range want {
		path := lintModPath + "/" + name
		got, err := changes.After().File(path).Contents(ctx)
		if err != nil {
			t.Errorf("reading %s from the changeset: %v", path, err)
			continue
		}
		if got != wantBody {
			t.Errorf("%s was not fixed as expected\nwant:\n%s\ngot:\n%s", path, wantBody, got)
		}
	}
}
