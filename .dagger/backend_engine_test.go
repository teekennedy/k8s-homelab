package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dagger/homelab/internal/cacheprobe"
	"dagger/homelab/internal/dagger"
)

// engineBackend is the real-engine half of the cache-granularity harness: it
// runs the same scenario table as the fake, so internal/daggerfake's model can
// be held against the thing it models. The tests it serves are in
// cache_test.go, alongside the fake-backed ones.
//
//	cd .dagger && dagger run go test ./...
//
// Without a session these tests skip; a plain `go test ./...` only exercises
// the fake. The Dagger function VerifyCacheGranularity runs them for you.
//
// # Two things the real engine forced on this design
//
// **Fixtures must come off a real filesystem.** Dagger keys exec caching on the
// call chain that produced a mount, and only collapses that chain to a content
// digest for directories loaded from a client filesystem. A source tree built
// in-process with dag.Directory().WithNewFile(...) stays a chain, so
// `source.Directory("a")` carries a fingerprint of its siblings and nothing
// caches per-module — an artefact of how the fixture was built, not of the
// module. So these tests write the fixture to disk and load it through
// CurrentWorkspace(), which is the same path `+defaultPath` takes in a real
// `dagger check`. The fake models the real path, not the in-process one.
//
// **A tree can't be edited in place.** The client filesystem sync is cached for
// the life of a session, so a mid-session edit is invisible. Each variant is
// planted at its own path instead; identical content still lands on the same
// content-addressed blob, which is exactly the property under test.
//
// # How a cache hit is observed
//
// The engine has no API for "was this exec cached", so the checks run against a
// container whose tools (`go`, `golangci-lint`) are shims that append a line to
// a file on a cache volume. Cache mounts are keyed by name and never take part
// in the exec's own key, so the shim observes without perturbing. A unit whose
// exec was served from cache leaves the line count untouched; one that re-ran
// adds a line. Line counts are the identity the scenarios compare.
//
// Using a shim also keeps the toolchain out of it: these tests never build the
// devenv container, so they cost seconds rather than a Nix build. That is also
// why buildsToolchain() is false — scenarios whose premise is the devenv
// container skip here and are answered by the fake alone.

// engineBackend runs the checks against a real engine.
type engineBackend struct {
	nonce   string
	volume  *dagger.CacheVolume
	planted int
	reads   int
}

// requireEngine skips a test that cannot run without a Dagger session. Shared
// so the two files that need it cannot drift into telling the reader two
// different things about how to get one.
func requireEngine(t *testing.T) {
	t.Helper()
	if !cacheprobe.RealSession() {
		t.Skip("no Dagger session; run under `dagger run go test ./...` to exercise the real engine")
	}
}

func newEngineBackend(t *testing.T) *engineBackend {
	t.Helper()
	requireEngine(t)

	// Unique per process, so every run starts from a cold cache and run 1 can
	// never be a hit left over from an earlier invocation.
	nonce := fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid())
	t.Cleanup(func() { _ = os.RemoveAll(fixtureRoot) })

	return &engineBackend{
		nonce:  nonce,
		volume: dag.CacheVolume("homelab-cachetest-" + nonce),
	}
}

func (e *engineBackend) name() string          { return "engine" }
func (e *engineBackend) fixture(c check) repo  { return c.fixture(e.nonce) }
func (e *engineBackend) buildsToolchain() bool { return false }

// fixtureRoot is where fixtures are planted, relative to the test binary's
// working directory. The Dagger client resolves CurrentWorkspace() paths
// against its own process's directory, so a plain relative path works wherever
// `dagger run` itself was invoked from.
const fixtureRoot = ".cachetest-tmp"

// plant writes a fixture to its own directory on disk and loads it the way the
// CLI loads a +defaultPath argument.
func (e *engineBackend) plant(t *testing.T, files repo) *dagger.Directory {
	t.Helper()
	e.planted++
	root := filepath.Join(fixtureRoot, e.nonce, fmt.Sprint(e.planted))

	for _, p := range sortedKeys(files) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("plant %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(files[p]), 0o600); err != nil {
			t.Fatalf("plant %s: %v", full, err)
		}
	}

	dir := dag.CurrentWorkspace().Directory(root)
	if _, err := dir.Entries(context.Background()); err != nil {
		t.Fatalf("the engine could not read the fixture at %s: %v\n"+
			"CurrentWorkspace() resolves paths against the test binary's working directory; "+
			"run the tests from the .dagger directory", root, err)
	}
	return dir
}

// shimContainer returns a throwaway container in which each of the check's
// tools is a script that records the fact it ran, rather than the real tool.
//
// The marker it records is the check's shimMarker, chosen to match what its
// unitOf derives on the fake side, so both backends key their results the same
// way.
func (e *engineBackend) shimContainer(c check) *dagger.Container {
	shim := "#!/bin/sh\nm=" + c.shimMarker + "\necho ran >> \"" + stampsPath + "/${m:-unknown}\"\nexit 0\n"

	ctr := dag.Container().
		From(shimImage).
		WithMountedCache(stampsPath, e.volume)
	for _, tool := range c.shimTools {
		ctr = ctr.WithNewFile("/usr/local/bin/"+tool, shim,
			dagger.ContainerWithNewFileOpts{Permissions: 0o755})
	}
	return ctr
}

const (
	shimImage  = "alpine:3.22"
	stampsPath = "/stamps"

	// markerFromMount names the module by the one .go file at the root of what
	// is mounted at /src — how GoModule.Test and GoModule.Lint scope each
	// module, and what unitByScopedGoModule reads on the fake side.
	markerFromMount = `$(basename "$(ls /src/*.go 2>/dev/null | head -1)")`

	// countLines prints "<marker>=<runs>" for every marker recorded so far.
	countLines = `cd ` + stampsPath +
		` && for f in *; do [ -e "$f" ] || continue; echo "$f=$(wc -l < "$f" | tr -d ' ')"; done`
)

// stamps reads the recorded line counts back out of the cache volume, as
// unit -> count. The reader is given a fresh env var each time, or the engine
// would serve the previous read's stdout from cache.
func (e *engineBackend) stamps(t *testing.T) units {
	t.Helper()
	e.reads++

	out, err := dag.Container().
		From(shimImage).
		WithMountedCache(stampsPath, e.volume).
		WithEnvVariable("READ", fmt.Sprintf("%s-%d", e.nonce, e.reads)).
		WithExec([]string{"sh", "-c", countLines}).
		Stdout(context.Background())
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
func (e *engineBackend) run(t *testing.T, c check, files repo) outcome {
	t.Helper()
	source := e.plant(t, files)
	if err := c.invoke(context.Background(), homelab(files), source, e.shimContainer(c)); err != nil {
		t.Fatalf("%s against the engine: %v", c.name, err)
	}
	return outcome{units: e.stamps(t)}
}
