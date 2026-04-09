package main

import (
	"dagger/homelab/pathutil"
)

// Re-export pattern sets for use in check functions.
var (
	validateWoodpeckerPatterns = pathutil.ValidateWoodpeckerPatterns
)

// Delegate to pathutil for pure functions.
var (
	filterPaths = pathutil.FilterPaths
)
