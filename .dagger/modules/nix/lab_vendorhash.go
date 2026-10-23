package main

import (
	"context"
	_ "embed"
	"fmt"

	"dagger/nix/internal/dagger"
)

//go:embed scripts/go-vendor-hash.sh
var goVendorHashScript string

const (
	// labModuleDir is the Go module whose nix buildGoModule vendorHash must
	// stay in sync with go.mod/go.sum.
	labModuleDir = "cmd/lab"
	// labFlakeAttr is the package attribute of the flake in labModuleDir.
	labFlakeAttr = "lab"
)

// Recomputes the lab CLI's Nix vendorHash in cmd/lab/gomod.json.
//
// buildGoModule pins the module's vendored dependencies by hash, so without
// this a change to a Go dependency of the lab CLI fails the Nix (and therefore
// devenv) build with a fixed-output hash mismatch.
// +generate
func (m *Lab) VendorHash(
	ctx context.Context,
	// buildGoModule vendors only the packages the source actually imports, so
	// the whole module directory is needed to produce the right hash.
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*", "cmd/lab/result", "cmd/lab/.direnv"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	// The hash is computed with the nixpkgs pinned by cmd/lab/flake.lock, while
	// devenv builds the same package with the nixpkgs pinned by the root
	// devenv.yaml. A change to buildGoModule's vendoring behaviour between the
	// two pins would make this pass while the devenv build still fails.
	//
	// Sync forces the exec here rather than when the caller evaluates the
	// changeset, so a script failure surfaces with its output attached.
	updated, err := m.Builders.build(ctx, nixImageStore, func(env nixEnv) *dagger.Container {
		return env.Container.
			WithMountedDirectory("/src", source).
			WithWorkdir("/src").
			WithNewFile("/go-vendor-hash.sh", goVendorHashScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
			WithExec(env.Exec([]string{"/go-vendor-hash.sh", labModuleDir, labFlakeAttr}))
	})
	if err == nil {
		updated, err = updated.Sync(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("recomputing %s vendor hash: %w", labModuleDir, err)
	}

	return updated.Directory("/src").Changes(source), nil
}
