package main

import (
	"context"
	"dagger/homelab/internal/dagger"
	_ "embed"
	"fmt"
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

// UpdateGoVendorHash recomputes the nix buildGoModule vendorHash for cmd/lab
// and writes it to cmd/lab/gomod.json. Run this whenever a Go dependency of
// the lab CLI changes, otherwise the nix (and therefore devenv) build fails
// with a fixed-output hash mismatch.
// Returns a changeset. Use `dagger call update-go-vendor-hash --auto-apply` to apply.
// +generate
func (m *Homelab) UpdateGoVendorHash(
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
	updated, err := nixContainer().
		WithMountedDirectory("/src", source).
		WithWorkdir("/src").
		WithNewFile("/go-vendor-hash.sh", goVendorHashScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithExec([]string{"/go-vendor-hash.sh", labModuleDir, labFlakeAttr}).
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("recomputing %s vendor hash: %w", labModuleDir, err)
	}

	return updated.Directory("/src").Changes(source), nil
}

// CheckGoVendorHash validates that cmd/lab/gomod.json holds the vendorHash nix
// actually computes for the lab CLI's dependencies.
// Use `dagger call update-go-vendor-hash --auto-apply` to fix.
// +check
func (m *Homelab) CheckGoVendorHash(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!cmd/lab/**/*", "cmd/lab/result", "cmd/lab/.direnv"]
	source *dagger.Directory,
) (string, error) {
	changeset, err := m.UpdateGoVendorHash(ctx, source)
	if err != nil {
		return "", err
	}

	empty, err := changeset.IsEmpty(ctx)
	if err != nil {
		return "", fmt.Errorf("checking go vendor hash changeset: %w", err)
	}

	if !empty {
		return "", fmt.Errorf("%s/gomod.json is out of date with go.mod/go.sum\nRun `dagger call update-go-vendor-hash --auto-apply` to fix", labModuleDir)
	}

	return "Go vendor hash is up to date", nil
}
