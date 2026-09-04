// Package cacheprobe makes the generated Dagger SDK importable from a plain
// `go test` binary.
//
// internal/dagger's init() reads DAGGER_SESSION_PORT and DAGGER_SESSION_TOKEN
// and panics when they are unset, so a test binary that links the module dies
// before TestMain ever runs. The panic is unconditional and happens in an
// imported package, so it cannot be recovered from — the only fix is to have
// the variables already set. Blank-importing this package from a _test.go file
// does that.
//
// The values are deliberately unusable: nothing in the test suite talks to a
// real engine. The tests replace the package-level `dag` client with one backed
// by internal/daggerfake, so the HTTP client this env builds is never dialled.
// The guard means a real `dagger call` (where the CLI sets both variables) is
// unaffected, and because only _test.go files import this package it is never
// linked into the module binary at all.
//
// # Why the init order works out
//
// Go does not specify the initialization order of two packages that don't
// import each other, but the linker does. cmd/link/internal/ld/inittask.go
// computes "an ordering of all of the inittask records so that the order
// respects all the dependencies, and given that restriction, orders the
// inittasks in lexicographic order": a topological sort that, at each step,
// pops the lexicographically smallest package whose imports are all scheduled.
//
// Two properties make this package win the race against internal/dagger:
//
//  1. Its only import is "os", which internal/dagger also imports. So at the
//     moment internal/dagger becomes schedulable this package already is — it
//     can never be blocked for longer.
//  2. "dagger/homelab/internal/cacheprobe" sorts before
//     "dagger/homelab/internal/dagger", so when both are ready this one pops
//     first.
//
// Keep the package name sorting before "dagger" and keep this file free of
// imports other than "os", and the ordering holds. If it ever stops holding,
// the symptom is loud: the panic above, naming DAGGER_SESSION_PORT.
package cacheprobe

import "os"

// faked records whether this package had to invent a session, i.e. whether the
// process was started outside `dagger run`.
var faked bool

// RealSession reports whether the process inherited a real Dagger session, and
// so whether `dag` can talk to an engine. Tests that need an engine skip when
// it returns false.
func RealSession() bool { return !faked }

// An init function is the whole point of this package: the environment has to
// be in place before any other package's init runs, which rules out TestMain.
//
//nolint:gochecknoinits // see the package doc; this must run before internal/dagger's own init
func init() {
	if os.Getenv("DAGGER_SESSION_PORT") != "" {
		return
	}
	// Errors here can only mean the runtime refused to set an environment
	// variable, in which case internal/dagger's init is about to panic with a
	// far clearer message than anything this package could report.
	faked = true
	_ = os.Setenv("DAGGER_SESSION_PORT", "1")
	_ = os.Setenv("DAGGER_SESSION_TOKEN", "offline-cacheprobe")
}
