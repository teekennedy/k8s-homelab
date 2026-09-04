package daggerfake

import (
	"context"
	"strings"
	"testing"

	"github.com/Khan/genqlient/graphql"
)

// These tests drive the Engine with hand-written GraphQL rather than through
// the Dagger SDK, so they stay independent of the module's own code and of the
// generated client. They pin the properties the cache assertions in
// dagger/homelab rely on: that a directory's id is a digest of its contents,
// that scoping to a subdirectory drops everything outside it, and that exec
// cache keys fold in the whole chain that produced them.

// query runs one GraphQL query and returns the leaf value, walking down the
// single-field-per-level response the SDK's query builder produces.
func query(t *testing.T, e *Engine, q string) any {
	t.Helper()
	var data any
	err := e.MakeRequest(context.Background(),
		&graphql.Request{Query: "query Query " + q},
		&graphql.Response{Data: &data})
	if err != nil {
		t.Fatalf("query failed: %v", err)
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

func queryString(t *testing.T, e *Engine, q string) string {
	t.Helper()
	s, ok := query(t, e, q).(string)
	if !ok {
		t.Fatalf("query did not return a string: %q", q)
	}
	return s
}

// dirQuery builds a directory holding the given files and appends a trailing
// selection, e.g. `id` or `glob(pattern:"**/go.mod")`.
func dirQuery(files map[string]string, tail string) string {
	var b strings.Builder
	b.WriteString("{directory")
	depth := 1
	for _, p := range sortedKeys(files) {
		b.WriteString(`{withNewFile(path:"` + p + `", contents:"` + files[p] + `")`)
		depth++
	}
	b.WriteString("{" + tail + "}")
	b.WriteString(strings.Repeat("}", depth))
	return b.String()
}

// ---------------------------------------------------------------------------
// content addressing
// ---------------------------------------------------------------------------

func TestDirectoryIDIsContentAddressed(t *testing.T) {
	files := map[string]string{"a/x.go": "one", "b/y.go": "two"}

	first := queryString(t, New(), dirQuery(files, "id"))
	// A fresh engine, and the files written in a different order, must still
	// produce the same id: the digest is over content, not over call order.
	same := queryString(t, New(), dirQuery(map[string]string{"b/y.go": "two", "a/x.go": "one"}, "id"))
	if first != same {
		t.Errorf("identical content produced different ids:\n%s\n%s", first, same)
	}

	changed := queryString(t, New(), dirQuery(map[string]string{"a/x.go": "one", "b/y.go": "CHANGED"}, "id"))
	if first == changed {
		t.Error("changing a file's contents did not change the directory id")
	}
}

// TestSubdirectoryIDIgnoresSiblings is the property the per-module cache
// argument depends on: GoModules() hands each module `source.Directory(path)`,
// and that scoped directory must not carry a fingerprint of the rest of the
// tree.
func TestSubdirectoryIDIgnoresSiblings(t *testing.T) {
	base := map[string]string{"a/x.go": "one", "b/y.go": "two"}
	sibling := map[string]string{"a/x.go": "one", "b/y.go": "CHANGED"}
	self := map[string]string{"a/x.go": "CHANGED", "b/y.go": "two"}

	const tail = `directory(path:"a"){id}`

	baseID := queryString(t, New(), dirQuery(base, tail))
	if got := queryString(t, New(), dirQuery(sibling, tail)); got != baseID {
		t.Error("editing b/ changed the id of the a/ subdirectory")
	}
	if got := queryString(t, New(), dirQuery(self, tail)); got == baseID {
		t.Error("editing a/ did not change the id of the a/ subdirectory")
	}
}

func TestGlobMatchesRootAndNestedPaths(t *testing.T) {
	files := map[string]string{
		"go.mod":              "root",
		"cmd/lab/go.mod":      "lab",
		"a/b/c/go.mod":        "deep",
		"cmd/lab/go.sum":      "not a match",
		"cmd/lab/nested.gomd": "not a match",
	}

	got, ok := query(t, New(), dirQuery(files, `glob(pattern:"**/go.mod")`)).([]any)
	if !ok {
		t.Fatal("glob did not return a list")
	}
	var paths []string
	for _, p := range got {
		s, ok := p.(string)
		if !ok {
			t.Fatalf("glob returned a non-string entry: %T", p)
		}
		paths = append(paths, s)
	}
	want := "a/b/c/go.mod,cmd/lab/go.mod,go.mod"
	if strings.Join(paths, ",") != want {
		t.Errorf("glob(**/go.mod)\nwant: %s\ngot:  %s", want, strings.Join(paths, ","))
	}
}

func TestMatchSegment(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main.gox", false},
		{"*.go", "main.go.go", true},
		{"go.mod", "go.mod", true},
		{"go.?od", "go.mod", true},
		{"*", "anything", true},
		{"a*b*c", "azzbzzc", true},
		{"a*b*c", "azzbzz", false},
	}
	for _, c := range cases {
		if got := matchSegment(c.pattern, c.name); got != c.want {
			t.Errorf("matchSegment(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// exec cache keys
// ---------------------------------------------------------------------------

// soleExecKey runs a query and returns the cache key of its single exec. Ids
// are only resolvable on the engine that issued them, so any id the query
// embeds must have been fetched from this same engine.
func soleExecKey(t *testing.T, e *Engine, q string) string {
	t.Helper()
	query(t, e, q)
	execs := e.Execs()
	if len(execs) != 1 {
		t.Fatalf("want 1 exec, got %d", len(execs))
	}
	return execs[0].CacheKey
}

func TestMountedContentReachesTheExecKey(t *testing.T) {
	key := func(contents string) string {
		e := New()
		id := queryString(t, e, dirQuery(map[string]string{"x.go": contents}, "id"))
		return soleExecKey(t, e, `{container{from(address:"alpine")`+
			`{withMountedDirectory(path:"/src", source:"`+id+`")`+
			`{withExec(args:["go","test","./..."]){sync}}}}}`)
	}

	first, repeat, changed := key("one"), key("one"), key("CHANGED")
	if first != repeat {
		t.Error("the same inputs produced different cache keys")
	}
	if first == changed {
		t.Error("changing the mounted directory's contents did not change the exec's cache key")
	}
}

// TestCacheMountNameKeysTheExec pins the BuildKit rule the model encodes: a
// cache volume is identified by name, and its mutable contents never invalidate
// the exec that mounts it. This is why the Go build and module caches in
// withToolchainCaches do not defeat the per-module isolation above.
func TestCacheMountNameKeysTheExec(t *testing.T) {
	key := func(volume string) string {
		e := New()
		id := queryString(t, e, `{cacheVolume(key:"`+volume+`"){id}}`)
		return soleExecKey(t, e, `{container{from(address:"alpine")`+
			`{withMountedCache(path:"/cache", cache:"`+id+`")`+
			`{withExec(args:["go","build","./..."]){sync}}}}}`)
	}

	first, repeat, other := key("homelab-go-build"), key("homelab-go-build"), key("homelab-go-mod")
	if first != repeat {
		t.Error("the same cache volume produced different exec keys")
	}
	if first == other {
		t.Error("a different cache volume name did not change the exec key")
	}
}

// TestExecKeysFoldInEverythingBefore pins the chaining rule behind a multi-step
// unit like GoModule.Lint's `go mod tidy` then `golangci-lint run`: an exec's
// key covers the whole state feeding it, so an earlier exec changing re-keys
// every later one.
func TestExecKeysFoldInEverythingBefore(t *testing.T) {
	chain := func(firstArg string) []Exec {
		e := New()
		query(t, e, `{container{from(address:"alpine")`+
			`{withExec(args:["first","`+firstArg+`"])`+
			`{withExec(args:["second"]){sync}}}}}`)
		return e.Execs()
	}

	before := chain("a")
	after := chain("b")
	if len(before) != 2 {
		t.Fatalf("want 2 execs, got %d", len(before))
	}
	if before[0].CacheKey == after[0].CacheKey {
		t.Error("changing the first exec's args did not change its own key")
	}
	if before[1].CacheKey == after[1].CacheKey {
		t.Error("changing the first exec did not re-key the second; chaining is not modelled")
	}
}

// TestExecOutputsAreKeyedByTheProducingExec covers Container.Directory and
// Container.File after an exec: the fake cannot know the bytes, and models the
// output's identity as the exec's cache key, which is what BuildKit reuses.
func TestExecOutputsAreKeyedByTheProducingExec(t *testing.T) {
	out := func(arg, tail string) string {
		return queryString(t, New(), `{container{from(address:"alpine")`+
			`{withExec(args:["build","`+arg+`"]){`+tail+`{id}}}}}`)
	}

	for _, tail := range []string{`directory(path:"/out")`, `file(path:"/out/bin")`} {
		first, repeat, other := out("a", tail), out("a", tail), out("b", tail)
		if first != repeat {
			t.Errorf("%s: the same exec produced different output ids", tail)
		}
		if first == other {
			t.Errorf("%s: a different exec produced the same output id", tail)
		}
	}
}

// TestUnsupportedFieldIsAnError makes sure the fake fails loudly on a call it
// does not model, rather than quietly returning a zero value that would show up
// as a spurious cache hit.
func TestUnsupportedFieldIsAnError(t *testing.T) {
	var data any
	err := New().MakeRequest(context.Background(),
		&graphql.Request{Query: `query Query {container{withNoSuchField(x:"y"){id}}}`},
		&graphql.Response{Data: &data})
	if err == nil {
		t.Fatal("want an error for an unmodelled field, got nil")
	}
	if !strings.Contains(err.Error(), "withNoSuchField") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}
