package main

import (
	"errors"
	"fmt"

	"dagger/go/internal/dagger"
)

// withExecOutput appends a failed exec's output to its error, which otherwise
// only carries the exit code.
func withExecOutput(err error) error {
	if execErr, ok := errors.AsType[*dagger.ExecError](err); ok {
		return fmt.Errorf("%w\n%s%s", err, execErr.Stdout, execErr.Stderr)
	}
	return err
}

// rerooted diffs a path-scoped directory against its changed version as a
// Changeset with repo-relative paths. Diffing the scoped directories directly
// yields paths relative to the unit ("main.go"), and `--auto-apply` — which
// writes relative to the repo root — would drop the file in the wrong place.
// Grafting each side onto an empty tree at path restores the repo-relative
// paths without widening what the exec had mounted.
func rerooted(path string, before, after *dagger.Directory) *dagger.Changeset {
	return dag.Directory().WithDirectory(path, after).
		Changes(dag.Directory().WithDirectory(path, before))
}
