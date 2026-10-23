package main

import (
	"testing"

	// Sets DAGGER_SESSION_PORT/TOKEN before internal/dagger's init() reads
	// them. Without it this test binary panics at startup.
	_ "dagger/lib/cacheprobe"
	"dagger/lib/doccheck"
)

// TestCheckDocs holds the module's checks and generators to the description
// format `dagger check -l` lists them by.
func TestCheckDocs(t *testing.T) { doccheck.Run(t, ".") }
