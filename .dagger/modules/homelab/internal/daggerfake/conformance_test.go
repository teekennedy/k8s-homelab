package daggerfake

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"dagger/homelab/internal/enginetest"

	"github.com/Khan/genqlient/graphql"
)

// The conformance tests send the same GraphQL to the fake and to a real engine
// and require the same answer from both: whether an exec is served from cache,
// and what a query that returns data returns. They are what make the
// cache-granularity tests' verdicts, which come from the fake alone, stand for
// the engine's.
//
// Every case also states the answer it expects, so the fake half runs in a
// plain `go test` and the engine half under enginetest's opt-in.
//
// A case covers a call the module's checks make, and how the engine keys it.
// When the fake learns a new call, or Dagger is upgraded, add or re-run cases
// here rather than testing the module's checks against the engine.

// tree is a source tree, path to contents.
type tree map[string]string

// base is the tree most cases start from: two modules and a config file at the
// root, like the repo the module checks.
var base = tree{
	".golangci.yaml": "version: \"2\"\n",
	"a/go.mod":       "module a\n",
	"a/main.go":      "package main\n",
	"b/go.mod":       "module b\n",
	"b/main.go":      "package main\n",
}

func (t tree) with(path, contents string) tree {
	next := tree{path: contents}
	for p, c := range t {
		if p != path {
			next[p] = c
		}
	}
	return next
}

const image = "alpine:3.22"

// ---------------------------------------------------------------------------
// the cases
// ---------------------------------------------------------------------------

// setup is what a case's query refers to: trees loaded the way `+defaultPath`
// loads one, then ids built from them, each a query whose leaf is an id. A
// later dep may refer to an earlier one. They are written $NAME in queries.
type setup struct {
	trees map[string]tree
	deps  [][2]string
}

// srcSetup loads t as $SRC.
func srcSetup(t tree, deps ...[2]string) setup {
	return setup{trees: map[string]tree{"SRC": t}, deps: deps}
}

// subdirA is $SRC's a/ as $A, the way a collection hands each unit its slice of
// the source.
var subdirA = [2]string{"A", `{node(id:$SRC){... on Directory{directory(path:"a"){id}}}}`}

// variant is one run of a cache case: a setup, and the container chain that
// leads to the exec being observed, as fields below `container`.
type variant struct {
	setup
	chain []string
}

type cacheCase struct {
	name          string
	before, after variant
	// rerun is whether the observed exec runs again for after, having run for
	// before.
	rerun bool
}

func run(s setup, chain ...string) variant { return variant{setup: s, chain: chain} }

// from starts a container chain.
func from(steps ...string) []string {
	return append([]string{`from(address:"` + image + `")`}, steps...)
}

func cacheCases() []cacheCase {
	mountSrc := from(`withMountedDirectory(path:"/src", source:$SRC)`)
	mountA := from(`withMountedDirectory(path:"/src", source:$A)`)
	mountCfg := from(`withMountedFile(path:"/cfg.yaml", source:$CFG)`)
	cfg := [2]string{"CFG", `{node(id:$SRC){... on Directory{file(path:".golangci.yaml"){id}}}}`}

	// graft builds a new directory from a/ and the config file, as helmfile
	// assembles a release's slice of the repo.
	graft := [2]string{"GRAFT", `{directory{withDirectory(path:"mod", source:$A)` +
		`{withFile(path:"cfg.yaml", source:$CFG){id}}}}`}
	mountGraft := from(`withMountedDirectory(path:"/src", source:$GRAFT)`)

	// output is a directory an earlier exec wrote, as Lint's changeset is
	// taken from.
	output := func(arg string) [2]string {
		return [2]string{"OUT", `{container{from(address:"` + image + `")` +
			`{withExec(args:["sh","-c","mkdir -p /out && echo ` + arg + ` > /out/x"])` +
			`{directory(path:"/out"){id}}}}}`}
	}
	mountOut := from(`withMountedDirectory(path:"/src", source:$OUT)`)

	vol := func(key string) [2]string { return [2]string{"VOL", `{cacheVolume(key:"` + key + `"){id}}`} }
	mountVol := from(`withMountedCache(path:"/cache", cache:$VOL)`)

	none := setup{}

	return []cacheCase{
		{
			name:   "an identical tree loaded from another path",
			before: run(srcSetup(base), mountSrc...),
			after:  run(srcSetup(base), mountSrc...),
		},
		{
			name:   "an edit to the mounted tree",
			before: run(srcSetup(base), mountSrc...),
			after:  run(srcSetup(base.with("b/main.go", "package b\n")), mountSrc...),
			rerun:  true,
		},
		{
			name:   "an edit beside the mounted subdirectory",
			before: run(srcSetup(base, subdirA), mountA...),
			after:  run(srcSetup(base.with("b/main.go", "package b\n"), subdirA), mountA...),
		},
		{
			name:   "an edit inside the mounted subdirectory",
			before: run(srcSetup(base, subdirA), mountA...),
			after:  run(srcSetup(base.with("a/main.go", "package a\n"), subdirA), mountA...),
			rerun:  true,
		},
		{
			name:   "an edit beside the mounted file",
			before: run(srcSetup(base, cfg), mountCfg...),
			after:  run(srcSetup(base.with("a/main.go", "package a\n"), cfg), mountCfg...),
		},
		{
			name:   "an edit to the mounted file",
			before: run(srcSetup(base, cfg), mountCfg...),
			after:  run(srcSetup(base.with(".golangci.yaml", "version: \"3\"\n"), cfg), mountCfg...),
			rerun:  true,
		},
		{
			name:   "an edit outside a grafted directory",
			before: run(srcSetup(base, subdirA, cfg, graft), mountGraft...),
			after:  run(srcSetup(base.with("b/main.go", "package b\n"), subdirA, cfg, graft), mountGraft...),
		},
		{
			name:   "an edit inside a grafted directory",
			before: run(srcSetup(base, subdirA, cfg, graft), mountGraft...),
			after:  run(srcSetup(base.with("a/go.mod", "module a2\n"), subdirA, cfg, graft), mountGraft...),
			rerun:  true,
		},
		{
			name:   "a file written into the container",
			before: run(none, from(`withNewFile(path:"/cfg", contents:"one")`)...),
			after:  run(none, from(`withNewFile(path:"/cfg", contents:"two")`)...),
			rerun:  true,
		},
		{
			name:   "an environment variable",
			before: run(none, from(`withEnvVariable(name:"X", value:"one")`)...),
			after:  run(none, from(`withEnvVariable(name:"X", value:"two")`)...),
			rerun:  true,
		},
		{
			name:   "the working directory",
			before: run(none, from(`withWorkdir(path:"/one")`)...),
			after:  run(none, from(`withWorkdir(path:"/two")`)...),
			rerun:  true,
		},
		{
			name:   "an earlier exec",
			before: run(none, from(`withExec(args:["true","one"])`)...),
			after:  run(none, from(`withExec(args:["true","two"])`)...),
			rerun:  true,
		},
		{
			name:   "an unchanged exec's output",
			before: run(setup{deps: [][2]string{output("one")}}, mountOut...),
			after:  run(setup{deps: [][2]string{output("one")}}, mountOut...),
		},
		{
			name:   "a changed exec's output",
			before: run(setup{deps: [][2]string{output("one")}}, mountOut...),
			after:  run(setup{deps: [][2]string{output("two")}}, mountOut...),
			rerun:  true,
		},
		{
			name:   "a cache volume of the same name",
			before: run(setup{deps: [][2]string{vol("conformance-a")}}, mountVol...),
			after:  run(setup{deps: [][2]string{vol("conformance-a")}}, mountVol...),
		},
		{
			name:   "a cache volume of another name",
			before: run(setup{deps: [][2]string{vol("conformance-a")}}, mountVol...),
			after:  run(setup{deps: [][2]string{vol("conformance-b")}}, mountVol...),
			rerun:  true,
		},
	}
}

// valueCase is a query that returns data rather than an id. The engine's answer
// is the reference; the fake's has to match it exactly.
type valueCase struct {
	name  string
	setup setup
	query string
	want  any
}

func valueCases() []valueCase {
	edited := base.with("a/main.go", "package a\n")
	same := [2]string{"SAME", `{node(id:$OTHER){... on Directory{changes(from:$SRC){id}}}}`}
	diff := [2]string{"DIFF", `{node(id:$EDITED){... on Directory{changes(from:$SRC){id}}}}`}
	return []valueCase{
		{
			name:  "glob across directories",
			setup: srcSetup(base),
			query: `{node(id:$SRC){... on Directory{glob(pattern:"**/go.mod")}}}`,
			want:  []any{"a/go.mod", "b/go.mod"},
		},
		{
			name:  "glob at the root",
			setup: srcSetup(base),
			query: `{node(id:$SRC){... on Directory{glob(pattern:"*.yaml")}}}`,
			want:  []any{".golangci.yaml"},
		},
		{
			name:  "glob in a subdirectory",
			setup: srcSetup(base),
			query: `{node(id:$SRC){... on Directory{directory(path:"a"){glob(pattern:"*")}}}}`,
			want:  []any{"go.mod", "main.go"},
		},
		{
			name:  "glob matching directories",
			setup: srcSetup(base),
			query: `{node(id:$SRC){... on Directory{glob(pattern:"*")}}}`,
			want:  []any{".golangci.yaml", "a/", "b/"},
		},
		{
			name:  "entries",
			setup: srcSetup(base),
			query: `{node(id:$SRC){... on Directory{entries}}}`,
			want:  []any{".golangci.yaml", "a/", "b/"},
		},
		{
			name:  "file contents",
			setup: srcSetup(base),
			query: `{node(id:$SRC){... on Directory{file(path:"a/go.mod"){contents}}}}`,
			want:  "module a\n",
		},
		{
			name:  "glob over a graft",
			setup: srcSetup(base, subdirA),
			query: `{directory{withDirectory(path:"x/y", source:$A){withNewFile(path:"z", contents:"z"){glob(pattern:"**")}}}}`,
			want:  []any{"x/", "x/y/", "x/y/go.mod", "x/y/main.go", "z"},
		},
		{
			name:  "changes between identical trees",
			setup: setup{trees: map[string]tree{"SRC": base, "OTHER": base}},
			query: `{node(id:$SRC){... on Directory{changes(from:$OTHER){isEmpty}}}}`,
			want:  true,
		},
		{
			name:  "changes between different trees",
			setup: setup{trees: map[string]tree{"SRC": edited, "OTHER": base}},
			query: `{node(id:$SRC){... on Directory{changes(from:$OTHER){isEmpty}}}}`,
			want:  false,
		},
		{
			name: "merging an empty changeset into an empty one",
			setup: setup{
				trees: map[string]tree{"SRC": base, "OTHER": base},
				deps:  [][2]string{same},
			},
			query: `{node(id:$SRC){... on Directory{changes(from:$OTHER){withChangeset(changes:$SAME){isEmpty}}}}}`,
			want:  true,
		},
		{
			name: "merging a non-empty changeset into an empty one",
			setup: setup{
				trees: map[string]tree{"SRC": base, "OTHER": base, "EDITED": edited},
				deps:  [][2]string{diff},
			},
			query: `{node(id:$SRC){... on Directory{changes(from:$OTHER){withChangesets(changes:[$DIFF]){isEmpty}}}}}`,
			want:  false,
		},
	}
}

// ---------------------------------------------------------------------------
// the backends
// ---------------------------------------------------------------------------

// side is one implementation of the API under test.
type side interface {
	client() graphql.Client
	// load returns the id of a directory holding t, loaded the way the CLI
	// loads a +defaultPath argument.
	load(t *testing.T, files tree) string
	// observe forces a container chain whose last step is the observed exec,
	// and returns an identity that is unchanged exactly when that exec was
	// served from cache.
	observe(t *testing.T, stamp string, chain []string) string
	// stamp returns the id of a cache volume unique to this case and process.
	stamp(t *testing.T, name string) string
}

type fakeSide struct{ engine *Engine }

func newFakeSide() *fakeSide { return &fakeSide{engine: New()} }

//nolint:ireturn // both sides are compared through the one interface
func (f *fakeSide) client() graphql.Client { return f.engine }

// load builds the tree in-process. The fake keys every directory by content, so
// this is its model of a directory loaded off a filesystem.
func (f *fakeSide) load(t *testing.T, files tree) string {
	t.Helper()
	fields := []string{"directory"}
	for _, p := range sortedKeys(files) {
		fields = append(fields, `withNewFile(path:`+strconv.Quote(p)+`, contents:`+strconv.Quote(files[p])+`)`)
	}
	return asString(t, request(t, f, nest(append(fields, "id")...)))
}

func (f *fakeSide) stamp(t *testing.T, name string) string {
	t.Helper()
	return asString(t, request(t, f, `{cacheVolume(key:`+strconv.Quote(name)+`){id}}`))
}

func (f *fakeSide) observe(t *testing.T, stamp string, chain []string) string {
	t.Helper()
	request(t, f, observedQuery(stamp, chain))
	execs := f.engine.Execs()
	return execs[len(execs)-1].CacheKey
}

// engineSide observes a real engine. The engine has no API for "was this exec
// cached", so the observed exec appends a line to a file on a cache volume:
// cache mounts are keyed by name and never by contents, so the file counts runs
// without taking part in what it counts.
type engineSide struct {
	gql     graphql.Client
	nonce   string
	root    string
	planted int
	reads   int
}

func newEngineSide(t *testing.T) *engineSide {
	t.Helper()
	return &engineSide{
		gql: enginetest.Client(t),
		// Unique per process, so the observed exec is cold on its first run
		// rather than a hit left over from an earlier one.
		nonce: fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()),
		root:  t.TempDir(),
	}
}

//nolint:ireturn // both sides are compared through the one interface
func (e *engineSide) client() graphql.Client { return e.gql }

// load writes the tree to a directory of its own and loads it back off the
// client filesystem. A tree cannot be edited in place: the engine caches a
// path's contents for the life of the session, so each tree gets a new path,
// and identical contents still land on the same content-addressed blob.
func (e *engineSide) load(t *testing.T, files tree) string {
	t.Helper()
	e.planted++
	root := filepath.Join(e.root, strconv.Itoa(e.planted))
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return asString(t, request(t, e, `{address(value:`+strconv.Quote(root)+`){directory{sync}}}`))
}

func (e *engineSide) stamp(t *testing.T, name string) string {
	t.Helper()
	key := "daggerfake-conformance-" + e.nonce + "-" + name
	return asString(t, request(t, e, `{cacheVolume(key:`+strconv.Quote(key)+`){id}}`))
}

func (e *engineSide) observe(t *testing.T, stamp string, chain []string) string {
	t.Helper()
	request(t, e, observedQuery(stamp, chain))

	// The reader gets a fresh env var each time, or it would be served its
	// previous stdout from cache.
	e.reads++
	out := request(t, e, nest("container", `from(address:"`+image+`")`,
		`withMountedCache(path:"/stamps", cache:`+strconv.Quote(stamp)+`)`,
		`withEnvVariable(name:"READ", value:"`+e.nonce+"-"+strconv.Itoa(e.reads)+`")`,
		`withExec(args:["sh","-c","cat /stamps/runs 2>/dev/null | wc -l"])`,
		"stdout"))
	return strings.TrimSpace(asString(t, out))
}

// observedQuery appends the stamping exec to a container chain.
func observedQuery(stamp string, chain []string) string {
	fields := append([]string{"container"}, chain...)
	fields = append(fields,
		`withMountedCache(path:"/stamps", cache:`+strconv.Quote(stamp)+`)`,
		`withExec(args:["sh","-c","echo ran >> /stamps/runs"])`,
		"sync")
	return nest(fields...)
}

// prepare loads a setup's trees and resolves its deps, returning the
// substitutions for its placeholders.
func prepare(t *testing.T, s side, set setup) *strings.Replacer {
	t.Helper()
	var pairs []string
	for _, name := range slices.Sorted(maps.Keys(set.trees)) {
		pairs = append(pairs, "$"+name, strconv.Quote(s.load(t, set.trees[name])))
	}
	for _, dep := range set.deps {
		q := strings.NewReplacer(pairs...).Replace(dep[1])
		pairs = append(pairs, "$"+dep[0], strconv.Quote(asString(t, request(t, s, q))))
	}
	return strings.NewReplacer(pairs...)
}

// ---------------------------------------------------------------------------
// the tests
// ---------------------------------------------------------------------------

func TestConformance(t *testing.T) {
	sides := map[string]func(*testing.T) side{
		"fake":   func(*testing.T) side { return newFakeSide() },
		"engine": func(t *testing.T) side { return newEngineSide(t) },
	}
	for _, name := range []string{"fake", "engine"} {
		t.Run(name, func(t *testing.T) {
			newSide := sides[name]
			t.Run("cache", func(t *testing.T) {
				for i, c := range cacheCases() {
					t.Run(c.name, func(t *testing.T) { checkCache(t, newSide(t), name, i, c) })
				}
			})
			t.Run("values", func(t *testing.T) {
				for _, c := range valueCases() {
					t.Run(c.name, func(t *testing.T) { checkValue(t, newSide(t), c) })
				}
			})
		})
	}
}

func checkCache(t *testing.T, s side, name string, i int, c cacheCase) {
	t.Helper()
	stamp := s.stamp(t, strconv.Itoa(i))
	runVariant := func(v variant) string {
		chain := strings.Join(v.chain, "\x00")
		chain = prepare(t, s, v.setup).Replace(chain)
		return s.observe(t, stamp, strings.Split(chain, "\x00"))
	}

	first := runVariant(c.before)
	repeat := runVariant(c.before)
	second := runVariant(c.after)

	// The controls: without them, a side that observed nothing, or cached
	// nothing, would agree with every case.
	if name == "engine" && first != "1" {
		t.Fatalf("the observed exec ran %s times on its first run, want 1", first)
	}
	if repeat != first {
		t.Fatalf("repeating the same run re-ran the exec (%s -> %s)", first, repeat)
	}

	if rerun := second != first; rerun != c.rerun {
		if c.rerun {
			t.Errorf("the exec was served from cache, want it re-run")
		} else {
			t.Errorf("the exec re-ran, want it served from cache (%s -> %s)", first, second)
		}
	}
}

func checkValue(t *testing.T, s side, c valueCase) {
	t.Helper()
	got := request(t, s, prepare(t, s, c.setup).Replace(c.query))
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(c.want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("want %s\ngot  %s", wantJSON, gotJSON)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// nest builds a query that selects each field inside the one before it.
func nest(fields ...string) string {
	q := fields[len(fields)-1]
	for i := len(fields) - 2; i >= 0; i-- {
		q = fields[i] + "{" + q + "}"
	}
	return "{" + q + "}"
}

// request runs a query and returns its leaf, walking down the one field per
// level that every query here selects.
func request(t *testing.T, s interface{ client() graphql.Client }, q string) any {
	t.Helper()
	var data any
	err := s.client().MakeRequest(context.Background(),
		&graphql.Request{Query: "query Conformance " + q, OpName: "Conformance"},
		&graphql.Response{Data: &data})
	if err != nil {
		t.Fatalf("%v\nquery: %s", err, q)
	}
	for {
		m, ok := data.(map[string]any)
		if !ok || len(m) != 1 {
			return data
		}
		for _, v := range m {
			data = v
		}
	}
}

func asString(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("want a string, got %T (%v)", v, v)
	}
	return s
}
