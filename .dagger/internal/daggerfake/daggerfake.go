// Package daggerfake is an in-process stand-in for the Dagger engine, built so
// this module's cache granularity can be asserted from a plain `go test` run.
//
// # What it models, and why that answers the caching question
//
// The Go SDK is lazy: every chained call appends to a querybuilder selection
// and nothing reaches the engine until a leaf field (id, sync, stdout, glob…)
// is selected. At that point the SDK emits the *entire* chain as one nested
// GraphQL query. That query is the DAG, verbatim — so intercepting
// graphql.Client is enough to see exactly what a check would ask the engine to
// do, without an engine.
//
// Engine implements graphql.Client and evaluates those queries against a value
// model of Directory, File, Container, CacheVolume and Changeset. Every object
// is content-addressed: its ID is a digest over its modelled state, with nested
// object references substituted by their own digests. A container's digest
// therefore folds in its base image, env, workdir and — the part that matters —
// the content digest of everything mounted into it.
//
// That is BuildKit's cache key, modelled at the level this module can control.
// A `WithExec` re-runs exactly when the digest of the container state feeding
// it differs from the previous run, so:
//
//	same Exec.CacheKey across two runs  ==  "CACHED" in `dagger check` output
//
// The fake never runs a command. Outputs of an exec (Container.Directory,
// Container.File) become *opaque* objects whose digest is derived from the
// producing container's digest and the path. That is the faithful model: with a
// cache hit BuildKit reuses the recorded result, so the output's identity is
// fully determined by the exec's cache key, never by bytes the fake would have
// to invent.
//
// # What it does not model
//
//   - `+ignore` and `+defaultPath`. Those are applied by the CLI when it loads
//     the caller's directory, before any of this module's code runs. Tests
//     construct the source directory explicitly and so choose their own scope.
//   - dagql's function-result cache, which sits above BuildKit. It keys on
//     content digests too, so the conclusions carry, but the unit asserted here
//     is the exec.
//   - Command semantics. An assertion that a check *passes* still needs a real
//     engine; these tests only assert what would and would not be re-run.
package daggerfake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/Khan/genqlient/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// Exec is one recorded WithExec: a unit of work the engine would either run or
// serve from cache.
type Exec struct {
	// Args is the argv the exec would run.
	Args []string
	// Workdir is the container's working directory at exec time.
	Workdir string
	// Mounts maps mount path to the content digest mounted there, for every
	// directory, file and cache mount visible to this exec.
	Mounts map[string]string
	// Files lists the paths inside each materialised directory mount, keyed by
	// mount path. Mounts whose content the fake cannot know (an earlier exec's
	// output, an image's filesystem) are absent.
	Files map[string][]string
	// CacheKey is the digest of the container state produced by this exec. Two
	// runs that yield the same CacheKey for an exec would hit the cache.
	CacheKey string
}

// Label renders the exec as "<workdir>$ <argv>". It is not unique: the same
// command run against two different mounts shares a label. Use MountsFile to
// tell those apart.
func (e Exec) Label() string {
	return e.Workdir + "$ " + strings.Join(e.Args, " ")
}

// MountsFile reports whether the directory mounted at mountPath contains a file
// at the given path relative to that mount.
func (e Exec) MountsFile(mountPath, file string) bool {
	for _, f := range e.Files[mountPath] {
		if f == file {
			return true
		}
	}
	return false
}

// Engine evaluates the GraphQL the Dagger Go SDK emits and records the execs it
// describes. It satisfies graphql.Client, so it can be injected in place of the
// real session client.
//
// It is safe for concurrent use: checks that fan out with an errgroup issue
// overlapping requests.
type Engine struct {
	mu    sync.Mutex
	objs  map[string]any
	execs []Exec
}

// New returns an Engine with an empty object registry and exec log.
func New() *Engine {
	return &Engine{objs: map[string]any{}}
}

// Execs returns the execs recorded so far, in the order they were evaluated.
func (e *Engine) Execs() []Exec {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Exec(nil), e.execs...)
}

// Engine satisfies graphql.Client, so it can be handed straight to
// querybuilder's Client() in place of the real session client.
var _ graphql.Client = (*Engine)(nil)

// MakeRequest implements graphql.Client.
func (e *Engine) MakeRequest(_ context.Context, req *graphql.Request, resp *graphql.Response) error {
	doc, err := parser.ParseQuery(&ast.Source{Input: req.Query})
	if err != nil {
		return fmt.Errorf("daggerfake: parse query: %w\n%s", err, req.Query)
	}
	if len(doc.Operations) != 1 {
		return fmt.Errorf("daggerfake: expected 1 operation, got %d", len(doc.Operations))
	}

	e.mu.Lock()
	data, err := e.evalSet(nil, doc.Operations[0].SelectionSet)
	e.mu.Unlock()
	if err != nil {
		return fmt.Errorf("daggerfake: %w\nquery: %s", err, req.Query)
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("daggerfake: marshal response: %w", err)
	}
	if err := json.Unmarshal(raw, resp.Data); err != nil {
		return fmt.Errorf("daggerfake: unmarshal response into %T: %w", resp.Data, err)
	}
	return nil
}

// evalSet walks one level of a selection set against parent, returning the
// response object for that level. Inline fragments are transparent: the SDK's
// unpack does not descend through them, so neither does this.
func (e *Engine) evalSet(parent any, set ast.SelectionSet) (map[string]any, error) {
	out := map[string]any{}
	for _, sel := range set {
		switch f := sel.(type) {
		case *ast.InlineFragment:
			inner, err := e.evalSet(parent, f.SelectionSet)
			if err != nil {
				return nil, err
			}
			for k, v := range inner {
				out[k] = v
			}
		case *ast.Field:
			val, err := e.evalField(parent, f)
			if err != nil {
				return nil, err
			}
			out[f.Alias] = val
		default:
			return nil, fmt.Errorf("unsupported selection %T", sel)
		}
	}
	return out, nil
}

// evalField resolves one field against parent, descending into its own
// selection set when it has one.
func (e *Engine) evalField(parent any, f *ast.Field) (any, error) {
	args, err := fieldArgs(f)
	if err != nil {
		return nil, err
	}
	val, err := e.apply(parent, f.Name, args)
	if err != nil {
		return nil, err
	}
	if len(f.SelectionSet) == 0 {
		return val, nil
	}
	return e.evalSet(val, f.SelectionSet)
}

func fieldArgs(f *ast.Field) (map[string]any, error) {
	args := map[string]any{}
	for _, a := range f.Arguments {
		v, err := a.Value.Value(nil)
		if err != nil {
			return nil, fmt.Errorf("argument %q of %q: %w", a.Name, f.Name, err)
		}
		args[a.Name] = v
	}
	return args, nil
}

// ---------------------------------------------------------------------------
// object model
// ---------------------------------------------------------------------------

// dirVal is a directory. A materialised directory knows its files (relative
// path to contents); an opaque one only knows the digest of whatever produced
// it, which is all the cache cares about.
type dirVal struct {
	files  map[string]string
	opaque string
}

// fileVal is a file, materialised or opaque, on the same terms as dirVal.
type fileVal struct {
	contents string
	opaque   string
}

// cacheVal is a cache volume. BuildKit keys these by name, not by content:
// a cache mount's contents never invalidate the exec that mounts it.
type cacheVal struct{ key string }

// ctrVal is a container, modelled as the ordered list of operations applied to
// it. Each step is already flattened to a string with nested objects replaced
// by their digests, so digesting the list is content-addressing.
type ctrVal struct {
	steps   []string
	workdir string
	// mounts and files track what an exec would see, for the Exec record.
	mounts map[string]string
	files  map[string][]string
}

// changesetVal is a changeset, modelled as the set of directory diffs merged
// into it. A diff is the pair of digests it was taken between, which is all the
// cache cares about; a merge (withChangeset/withChangesets) concatenates pairs
// rather than computing a tree, because the fake never knows the bytes on the
// "after" side of an exec.
type changesetVal struct{ pairs [][2]string }

func digest(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return kind + ":" + hex.EncodeToString(h.Sum(nil))[:32]
}

func (d *dirVal) digest() string {
	if d.opaque != "" {
		return digest("dir", "opaque", d.opaque)
	}
	parts := make([]string, 0, len(d.files)*2)
	for _, p := range sortedKeys(d.files) {
		parts = append(parts, p, d.files[p])
	}
	return digest("dir", parts...)
}

func (f *fileVal) digest() string {
	if f.opaque != "" {
		return digest("file", "opaque", f.opaque)
	}
	return digest("file", f.contents)
}

func (c *cacheVal) digest() string { return digest("cache", c.key) }
func (c *ctrVal) digest() string   { return digest("ctr", c.steps...) }

func (c *changesetVal) digest() string {
	parts := make([]string, 0, len(c.pairs)*2)
	for _, p := range c.pairs {
		parts = append(parts, p[0], p[1])
	}
	return digest("changeset", parts...)
}

// empty reports whether every diff merged into this changeset is between two
// identical directories. An empty changeset (no diffs at all) is empty too.
func (c *changesetVal) empty() bool {
	for _, p := range c.pairs {
		if p[0] != p[1] {
			return false
		}
	}
	return true
}

// merge returns a changeset carrying this one's diffs followed by others'.
func (c *changesetVal) merge(others ...*changesetVal) *changesetVal {
	next := &changesetVal{pairs: append([][2]string(nil), c.pairs...)}
	for _, o := range others {
		next.pairs = append(next.pairs, o.pairs...)
	}
	return next
}

func digestOf(v any) (string, error) {
	switch o := v.(type) {
	case *dirVal:
		return o.digest(), nil
	case *fileVal:
		return o.digest(), nil
	case *cacheVal:
		return o.digest(), nil
	case *ctrVal:
		return o.digest(), nil
	case *changesetVal:
		return o.digest(), nil
	default:
		return "", fmt.Errorf("cannot take the id of %T", v)
	}
}

// register records an object under its content-addressed id so a later
// node(id:) lookup, or an object passed as an argument, resolves back to it.
func (e *Engine) register(v any) (string, error) {
	id, err := digestOf(v)
	if err != nil {
		return "", err
	}
	e.objs[id] = v
	return id, nil
}

func (e *Engine) lookup(id string) (any, error) {
	v, ok := e.objs[id]
	if !ok {
		return nil, fmt.Errorf("unknown id %q", id)
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// field dispatch
// ---------------------------------------------------------------------------

func (e *Engine) apply(parent any, field string, args map[string]any) (any, error) {
	switch p := parent.(type) {
	case nil:
		return e.applyQuery(field, args)
	case *dirVal:
		return e.applyDirectory(p, field, args)
	case *fileVal:
		return e.applyFile(p, field, args)
	case *ctrVal:
		return e.applyContainer(p, field, args)
	case *cacheVal:
		return e.applyLeaf(p, field)
	case *changesetVal:
		return e.applyChangeset(p, field, args)
	default:
		return nil, fmt.Errorf("cannot select %q on %T", field, parent)
	}
}

// applyLeaf handles the fields every object shares.
func (e *Engine) applyLeaf(v any, field string) (any, error) {
	switch field {
	case "id", "sync":
		return e.register(v)
	default:
		return nil, fmt.Errorf("unsupported field %q on %T", field, v)
	}
}

// applyChangeset covers the fields a Changeset is forced through. Note that
// isEmpty is the one that makes the real engine actually run the execs behind
// the changeset — Changeset.sync does not — so it is what the tests use.
func (e *Engine) applyChangeset(c *changesetVal, field string, args map[string]any) (any, error) {
	switch field {
	case "id", "sync":
		return e.register(c)

	case "isEmpty":
		return c.empty(), nil

	case "withChangeset":
		other, err := e.changesetArg(args["changes"], "withChangeset changes")
		if err != nil {
			return nil, err
		}
		return c.merge(other), nil

	case "withChangesets":
		raw, ok := args["changes"].([]any)
		if !ok {
			return nil, fmt.Errorf("withChangesets changes: expected a list, got %T", args["changes"])
		}
		others := make([]*changesetVal, 0, len(raw))
		for i, id := range raw {
			other, err := e.changesetArg(id, fmt.Sprintf("withChangesets changes[%d]", i))
			if err != nil {
				return nil, err
			}
			others = append(others, other)
		}
		return c.merge(others...), nil

	default:
		return nil, fmt.Errorf("unsupported Changeset field %q", field)
	}
}

// changesetArg resolves an argument the SDK marshalled as a Changeset id.
func (e *Engine) changesetArg(raw any, what string) (*changesetVal, error) {
	v, err := e.arg(raw, what)
	if err != nil {
		return nil, err
	}
	cs, ok := v.(*changesetVal)
	if !ok {
		return nil, fmt.Errorf("%s is %T", what, v)
	}
	return cs, nil
}

func (e *Engine) applyQuery(field string, args map[string]any) (any, error) {
	switch field {
	case "directory":
		return &dirVal{files: map[string]string{}}, nil
	case "changeset":
		return &changesetVal{}, nil
	case "container":
		return newContainer(), nil
	case "cacheVolume":
		return &cacheVal{key: str(args["key"])}, nil
	case "node":
		return e.lookup(str(args["id"]))
	case "loadDirectoryFromID", "loadFileFromID", "loadContainerFromID", "loadCacheVolumeFromID", "loadChangesetFromID":
		return e.lookup(str(args["id"]))
	case "defaultPlatform":
		return "linux/amd64", nil
	case "version":
		return "daggerfake", nil
	default:
		return nil, fmt.Errorf("unsupported Query field %q", field)
	}
}

// applyDirectory is a flat dispatch over Directory's GraphQL fields. It reads
// as a table because that is what it is; grouping the cases into helpers to
// satisfy a complexity budget would only hide the one-to-one mapping with the
// schema.
//
//nolint:cyclop // GraphQL field dispatch: one case per schema field
func (e *Engine) applyDirectory(d *dirVal, field string, args map[string]any) (any, error) {
	switch field {
	case "id", "sync":
		return e.register(d)

	case "withNewFile":
		next := d.clone()
		if err := next.materialised("withNewFile"); err != nil {
			return nil, err
		}
		next.files[clean(str(args["path"]))] = str(args["contents"])
		return next, nil

	case "withFile":
		f, err := e.arg(args["source"], "withFile source")
		if err != nil {
			return nil, err
		}
		file, ok := f.(*fileVal)
		if !ok {
			return nil, fmt.Errorf("withFile source is %T", f)
		}
		next := d.clone()
		if err := next.materialised("withFile"); err != nil {
			return nil, err
		}
		if file.opaque != "" {
			return nil, fmt.Errorf("withFile of an opaque file is not modelled")
		}
		next.files[clean(str(args["path"]))] = file.contents
		return next, nil

	case "withDirectory":
		src, err := e.arg(args["source"], "withDirectory source")
		if err != nil {
			return nil, err
		}
		sub, ok := src.(*dirVal)
		if !ok {
			return nil, fmt.Errorf("withDirectory source is %T", src)
		}
		at := clean(str(args["path"]))
		// Grafting a directory whose contents the fake cannot know — an exec's
		// output, say, as GoModule.Lint re-roots its /src under the module path
		// — yields an opaque result rather than an error. Its identity still
		// folds in both sides, which is all a cache assertion needs; what is
		// lost is the file listing, and callers that need one say so by
		// selecting entries or glob, which reject an opaque directory.
		if sub.opaque != "" || d.opaque != "" {
			return &dirVal{opaque: digest("graft", d.digest(), at, sub.digest())}, nil
		}
		next := d.clone()
		if err := next.materialised("withDirectory"); err != nil {
			return nil, err
		}
		for p, c := range sub.files {
			next.files[clean(path.Join(at, p))] = c
		}
		return next, nil

	case "directory":
		if d.opaque != "" {
			return &dirVal{opaque: digest("subdir", d.opaque, clean(str(args["path"])))}, nil
		}
		prefix := clean(str(args["path"])) + "/"
		sub := &dirVal{files: map[string]string{}}
		for p, c := range d.files {
			if rest, ok := strings.CutPrefix(p, prefix); ok {
				sub.files[rest] = c
			}
		}
		return sub, nil

	case "file":
		p := clean(str(args["path"]))
		if d.opaque != "" {
			return &fileVal{opaque: digest("subfile", d.opaque, p)}, nil
		}
		c, ok := d.files[p]
		if !ok {
			return nil, fmt.Errorf("no such file %q in directory", p)
		}
		return &fileVal{contents: c}, nil

	case "entries":
		if d.opaque != "" {
			return nil, fmt.Errorf("entries of an opaque directory is not modelled")
		}
		return sortedKeys(d.files), nil

	case "glob":
		if d.opaque != "" {
			return nil, fmt.Errorf("glob of an opaque directory is not modelled")
		}
		return glob(sortedKeys(d.files), str(args["pattern"])), nil

	case "changes":
		from, err := e.arg(args["from"], "changes from")
		if err != nil {
			return nil, err
		}
		fromDir, ok := from.(*dirVal)
		if !ok {
			return nil, fmt.Errorf("changes from is %T", from)
		}
		return &changesetVal{pairs: [][2]string{{fromDir.digest(), d.digest()}}}, nil

	default:
		return nil, fmt.Errorf("unsupported Directory field %q", field)
	}
}

func (e *Engine) applyFile(f *fileVal, field string, args map[string]any) (any, error) {
	switch field {
	case "id", "sync":
		return e.register(f)
	case "contents":
		if f.opaque != "" {
			return nil, fmt.Errorf("contents of an opaque file is not modelled")
		}
		return f.contents, nil
	case "name":
		return path.Base(clean(str(args["path"]))), nil
	default:
		return nil, fmt.Errorf("unsupported File field %q", field)
	}
}

func newContainer() *ctrVal {
	return &ctrVal{mounts: map[string]string{}, files: map[string][]string{}}
}

func (c *ctrVal) clone() *ctrVal {
	next := &ctrVal{
		steps:   append([]string(nil), c.steps...),
		workdir: c.workdir,
		mounts:  make(map[string]string, len(c.mounts)),
		files:   make(map[string][]string, len(c.files)),
	}
	for k, v := range c.mounts {
		next.mounts[k] = v
	}
	for k, v := range c.files {
		next.files[k] = v
	}
	return next
}

func (c *ctrVal) step(s string) *ctrVal {
	next := c.clone()
	next.steps = append(next.steps, s)
	return next
}

// applyContainer is a flat dispatch over Container's GraphQL fields, on the
// same terms as applyDirectory.
//
//nolint:cyclop // GraphQL field dispatch: one case per schema field
func (e *Engine) applyContainer(c *ctrVal, field string, args map[string]any) (any, error) {
	switch field {
	case "id", "sync":
		return e.register(c)

	case "from":
		return c.step("from " + str(args["address"])), nil

	case "withUser":
		return c.step("user " + str(args["name"])), nil

	case "withWorkdir":
		next := c.step("workdir " + clean(str(args["path"])))
		next.workdir = clean(str(args["path"]))
		return next, nil

	case "withEnvVariable":
		return c.step(fmt.Sprintf("env %s=%s expand=%v",
			str(args["name"]), str(args["value"]), args["expand"] == true)), nil

	case "withoutEnvVariable":
		return c.step("unset-env " + str(args["name"])), nil

	case "withEntrypoint":
		return c.step("entrypoint " + strings.Join(strSlice(args["args"]), " ")), nil

	case "import":
		f, err := e.arg(args["source"], "import source")
		if err != nil {
			return nil, err
		}
		file, ok := f.(*fileVal)
		if !ok {
			return nil, fmt.Errorf("import source is %T", f)
		}
		return c.step("import " + file.digest()), nil

	case "withMountedDirectory", "withDirectory":
		src, err := e.arg(args["source"], field+" source")
		if err != nil {
			return nil, err
		}
		dir, ok := src.(*dirVal)
		if !ok {
			return nil, fmt.Errorf("%s source is %T", field, src)
		}
		at := clean(str(args["path"]))
		next := c.step(fmt.Sprintf("%s %s=%s", field, at, dir.digest()))
		next.mounts[at] = dir.digest()
		if dir.opaque == "" {
			next.files[at] = sortedKeys(dir.files)
		}
		return next, nil

	case "withMountedFile", "withFile":
		src, err := e.arg(args["source"], field+" source")
		if err != nil {
			return nil, err
		}
		file, ok := src.(*fileVal)
		if !ok {
			return nil, fmt.Errorf("%s source is %T", field, src)
		}
		at := clean(str(args["path"]))
		next := c.step(fmt.Sprintf("%s %s=%s", field, at, file.digest()))
		next.mounts[at] = file.digest()
		return next, nil

	case "withMountedCache":
		cv, err := e.arg(args["cache"], "withMountedCache cache")
		if err != nil {
			return nil, err
		}
		vol, ok := cv.(*cacheVal)
		if !ok {
			return nil, fmt.Errorf("withMountedCache cache is %T", cv)
		}
		// A cache mount's *contents* are deliberately excluded from the key:
		// BuildKit identifies the volume by name and lets execs mutate it, so
		// it never invalidates the exec that mounts it. Its seed source does
		// take part, because that is baked in when the volume is created.
		seed := ""
		if raw, ok := args["source"]; ok && raw != nil {
			src, err := e.arg(raw, "withMountedCache source")
			if err != nil {
				return nil, err
			}
			dir, ok := src.(*dirVal)
			if !ok {
				return nil, fmt.Errorf("withMountedCache source is %T", src)
			}
			seed = dir.digest()
		}
		at := clean(str(args["path"]))
		next := c.step(fmt.Sprintf("withMountedCache %s=%s seed=%s", at, vol.key, seed))
		next.mounts[at] = "cache:" + vol.key
		return next, nil

	case "withUnixSocket":
		return c.step("socket " + clean(str(args["path"]))), nil

	case "withExec":
		argv := strSlice(args["args"])
		next := c.step("exec " + strings.Join(argv, "\x1f"))
		e.execs = append(e.execs, Exec{
			Args:     argv,
			Workdir:  next.workdir,
			Mounts:   copyStrMap(next.mounts),
			Files:    copyStrSliceMap(next.files),
			CacheKey: next.digest(),
		})
		return next, nil

	case "directory":
		return &dirVal{opaque: digest("ctr-dir", c.digest(), clean(str(args["path"])))}, nil

	case "file":
		return &fileVal{opaque: digest("ctr-file", c.digest(), clean(str(args["path"])))}, nil

	case "stdout", "stderr":
		return "", nil

	case "exitCode":
		return 0, nil

	default:
		return nil, fmt.Errorf("unsupported Container field %q", field)
	}
}

// arg resolves an argument that the SDK marshalled as an object id.
func (e *Engine) arg(raw any, what string) (any, error) {
	id, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%s: expected an id string, got %T", what, raw)
	}
	v, err := e.lookup(id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return v, nil
}

func (d *dirVal) clone() *dirVal {
	next := &dirVal{opaque: d.opaque}
	if d.files != nil {
		next.files = make(map[string]string, len(d.files))
		for k, v := range d.files {
			next.files[k] = v
		}
	}
	return next
}

// materialised rejects mutations of a directory whose contents the fake cannot
// know, rather than silently inventing an empty tree.
func (d *dirVal) materialised(op string) error {
	if d.opaque != "" {
		return fmt.Errorf("%s on an opaque directory is not modelled", op)
	}
	if d.files == nil {
		d.files = map[string]string{}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func str(v any) string {
	s, _ := v.(string)
	return s
}

func strSlice(v any) []string {
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		out = append(out, str(x))
	}
	return out
}

func clean(p string) string {
	return strings.TrimPrefix(path.Clean("/"+p), "/")
}

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

func copyStrMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyStrSliceMap(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// glob applies Dagger's Directory.glob semantics to a file list: "*" matches
// within a path segment, "**" matches across segments, and a leading "**/"
// also matches paths with no directory at all.
func glob(paths []string, pattern string) []string {
	var out []string
	for _, p := range paths {
		if matchGlob(pattern, p) {
			out = append(out, p)
		}
	}
	return out
}

func matchGlob(pattern, name string) bool {
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		// "**/" matches zero or more leading segments.
		if matchGlob(rest, name) {
			return true
		}
	}
	pat := strings.Split(pattern, "/")
	seg := strings.Split(name, "/")
	return matchSegments(pat, seg)
}

func matchSegments(pat, seg []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(seg); i++ {
				if matchSegments(pat[1:], seg[i:]) {
					return true
				}
			}
			return false
		}
		if len(seg) == 0 || !matchSegment(pat[0], seg[0]) {
			return false
		}
		pat, seg = pat[1:], seg[1:]
	}
	return len(seg) == 0
}

// matchSegment matches a single path segment against a pattern that may contain
// "*" (any run of characters) and "?" (one character).
//
//nolint:cyclop // a wildcard matcher's state machine is clearer whole than split
func matchSegment(pat, s string) bool {
	// Iterative backtracking match, so a pattern with several "*" stays linear
	// in practice and cannot recurse deeply on adversarial input.
	var pi, si, star, mark int
	star = -1
	for si < len(s) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == s[si]):
			pi++
			si++
		case pi < len(pat) && pat[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}
