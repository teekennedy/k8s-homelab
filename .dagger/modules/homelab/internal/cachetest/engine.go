package cachetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dagger/homelab/internal/cacheprobe"
)

// engineBackend is the real-engine half of the harness: it runs the same
// scenario table as the fake, so daggerfake's model can be held against the
// thing it models.
//
//	cd .dagger/modules/<module> && HOMELAB_ENGINE_TESTS=1 dagger run go test ./...
//
// Without that opt-in and a session these tests skip; a plain `go test ./...`
// only exercises the fake. ci:verify-cache-granularity runs them for you.
//
// # Two things the real engine forced on this design
//
// **Fixtures must come off a real filesystem.** Dagger keys exec caching on the
// call chain that produced a mount, and only collapses that chain to a content
// digest for directories loaded from a client filesystem. A source tree built
// in-process with dag.Directory().WithNewFile(...) stays a chain, so
// `source.Directory("a")` carries a fingerprint of its siblings and nothing
// caches per-unit — an artefact of how the fixture was built, not of the
// module. So these tests write the fixture to disk and load it back off the
// client filesystem, which is what `+defaultPath` does in a real `dagger check`.
// The fake models that path, not the in-process one.
//
// The load goes through dag.Address(), not CurrentWorkspace(): a workspace
// resolves relative paths from the workspace directory and absolute ones from
// the workspace boundary, so it cannot name a t.TempDir() outside the repo,
// whereas an address takes the host path as given. Both reach the same
// Host.directory op, and a directory loaded either way content-addresses
// identically — which is the property under test here.
//
// **A tree can't be edited in place.** The client filesystem sync is cached for
// the life of a session, so a mid-session edit is invisible. Each variant is
// planted at its own path instead; identical content still lands on the same
// content-addressed blob, which is exactly the property under test.
//
// # How a cache hit is observed
//
// The engine has no API for "was this exec cached", so the checks run against a
// container whose tools (`go`, `helmfile`, …) are shims that append a line to a
// file on a cache volume. Cache mounts are keyed by name and never take part in
// the exec's own key, so the shim observes without perturbing. A unit whose
// exec was served from cache leaves the line count untouched; one that re-ran
// adds a line. Line counts are the identity the scenarios compare.
//
// Using a shim also keeps the toolchain out of it: these tests never build the
// devenv container, so they cost seconds rather than a Nix build.
type engineBackend[D, C any] struct {
	client  Client[D, C]
	nonce   string
	root    string
	volume  string
	planted int
	reads   int
}

// EngineTestsEnv opts a test run in to the tests that drive a real engine.
//
// A Dagger session alone is not enough to say so: every container Dagger runs
// has one, so `go:test` would run them too, with only the module under test
// mounted and twice over with ci:verify-cache-granularity.
const EngineTestsEnv = "HOMELAB_ENGINE_TESTS"

// RequireEngine skips a test that drives a real engine, unless the run opted in
// with EngineTestsEnv=1 under a Dagger session.
func RequireEngine(t *testing.T) {
	t.Helper()
	if os.Getenv(EngineTestsEnv) != "1" {
		t.Skip(EngineTestsEnv + " is not set; run `dagger check ci:verify-cache-granularity` to exercise the real engine")
	}
	if !cacheprobe.RealSession() {
		t.Skip("no Dagger session; run under `" + EngineTestsEnv + "=1 dagger run go test ./...`")
	}
}

func newEngineBackend[D, C any](t *testing.T, client Client[D, C]) *engineBackend[D, C] {
	t.Helper()
	RequireEngine(t)

	// Unique per process, so every run starts from a cold cache and run 1 can
	// never be a hit left over from an earlier invocation.
	nonce := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())

	return &engineBackend[D, C]{
		client: client,
		nonce:  nonce,
		root:   t.TempDir(),
		volume: "homelab-cachetest-" + nonce,
	}
}

func (*engineBackend[D, C]) name() string                 { return "engine" }
func (e *engineBackend[D, C]) fixture(c Check[D, C]) Repo { return c.Fixture(e.nonce) }

// plant writes a fixture to its own directory under the test's temp dir and
// loads it the way the CLI loads a +defaultPath argument.
//
//nolint:ireturn // D is the module's *dagger.Directory, a concrete pointer type
func (e *engineBackend[D, C]) plant(t *testing.T, files Repo) D {
	t.Helper()
	e.planted++
	root := filepath.Join(e.root, fmt.Sprint(e.planted))

	for _, p := range sortedKeys(files) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("plant %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(files[p]), 0o600); err != nil {
			t.Fatalf("plant %s: %v", full, err)
		}
	}

	dir, err := e.client.HostDirectory(context.Background(), root)
	if err != nil {
		t.Fatalf("the engine could not read the fixture at %s: %v\n"+
			"the address is an absolute host path, so the client has to be able to read it "+
			"— check TMPDIR is visible to the process holding the Dagger session", root, err)
	}
	return dir
}

const (
	shimImage  = "alpine:3.22"
	stampsPath = "/stamps"

	// countLines prints "<marker>=<runs>" for every marker recorded so far.
	countLines = `cd ` + stampsPath +
		` && for f in *; do [ -e "$f" ] || continue; echo "$f=$(wc -l < "$f" | tr -d ' ')"; done`
)

// shimContainer returns a throwaway container in which each of the check's
// tools is a script that records the fact it ran, rather than the real tool.
//
// The marker it records is the check's ShimMarker, chosen to match what its
// UnitOf derives on the fake side, so both backends key their results the same
// way.
//
//nolint:ireturn // C is the module's *dagger.Container, a concrete pointer type
func (e *engineBackend[D, C]) shimContainer(c Check[D, C]) C {
	shim := "#!/bin/sh\nm=" + c.ShimMarker + "\necho ran >> \"" + stampsPath + "/${m:-unknown}\"\nexit 0\n"
	tools := make([]File, len(c.ShimTools))
	for i, tool := range c.ShimTools {
		tools[i] = File{Path: "/usr/local/bin/" + tool, Contents: shim}
	}
	return e.client.Container(shimImage, stampsPath, e.volume, tools)
}

// stamps reads the recorded line counts back out of the cache volume, as
// unit -> count. The reader is given a fresh env var each time, or the engine
// would serve the previous read's stdout from cache.
func (e *engineBackend[D, C]) stamps(t *testing.T) units {
	t.Helper()
	e.reads++

	out, err := e.client.Stdout(context.Background(), shimImage, stampsPath, e.volume,
		fmt.Sprintf("%s-%d", e.nonce, e.reads), countLines)
	if err != nil {
		t.Fatalf("reading stamps: %v", err)
	}

	counts := units{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if name, count, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			counts[name] = count
		}
	}
	return counts
}

// run invokes the check against a planted fixture and reports the stamp counts
// as the units' identities: a count that moved is a unit that re-ran.
func (e *engineBackend[D, C]) run(t *testing.T, c Check[D, C], files Repo) outcome {
	t.Helper()
	source := e.plant(t, files)
	if err := c.Invoke(context.Background(), source, e.shimContainer(c)); err != nil {
		t.Fatalf("%s against the engine: %v", c.Name, err)
	}
	return outcome{units: e.stamps(t)}
}
