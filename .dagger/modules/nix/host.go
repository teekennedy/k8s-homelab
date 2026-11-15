// Operates on one NixOS host over SSH: shows what a deploy would change, and
// bootstraps and installs a new host.
//
// These reach live hosts, so unlike the checks every call runs afresh rather
// than from Dagger's cache. They log in with the module's sshAuthSock,
// sshConfig and sshUser settings, and know the hosts by the module's
// sshKnownHosts.

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dagger/nix/internal/dagger"
)

// NixosHost operates on one NixOS host of the flake.
type NixosHost struct {
	// The host's name in the flake's nixosConfigurations, which is also how
	// SSH reaches it.
	Name string
	// +private
	Remote *Builders
}

// Host operates on a NixOS host: diff a deploy, bootstrap or install it.
func (m *Nix) Host(
	// The host's name in the flake's nixosConfigurations, such as borg-0.
	name string,
) *NixosHost {
	return &NixosHost{Name: name, Remote: m.Remote}
}

// toplevel is the flake attribute of the host's system.
func (h *NixosHost) toplevel() string {
	return ".#nixosConfigurations." + h.Name + ".config.system.build.toplevel"
}

// container is the store-backed Nix image with source at /src and an ssh
// client that reaches the hosts through the forwarded agent. Nothing after it
// is cached: the hosts it talks to change underneath it.
func (h *NixosHost) container(source *dagger.Directory) (*dagger.Container, error) {
	if h.Remote == nil || h.Remote.SSHAuthSock == nil {
		return nil, errors.New("no SSH agent forwarded: set the nix module's sshAuthSock setting (see .dagger/docs/nix-builders.md)")
	}
	return h.Remote.withSSH(nixImageStore(false)).
		WithEnvVariable("HOMELAB_HOST_RUN", time.Now().Format(time.RFC3339Nano)).
		WithDirectory("/src", source).
		WithWorkdir("/src"), nil
}

// withTools puts nixpkgs packages on c's PATH, from the nixpkgs the flake
// pins, so they match what the hosts run.
func withTools(ctx context.Context, c *dagger.Container, pkgs ...string) (*dagger.Container, error) {
	args := nixCommand("build", "--no-link", "--print-out-paths", "--inputs-from", "/src")
	for _, p := range pkgs {
		args = append(args, "nixpkgs#"+p)
	}
	out, err := c.WithExec(args).Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("building %s: %w", strings.Join(pkgs, ", "), withExecOutput(err))
	}
	path, err := c.EnvVariable(ctx, "PATH")
	if err != nil {
		return nil, fmt.Errorf("reading PATH: %w", err)
	}
	var bins []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		bins = append(bins, line+"/bin")
	}
	return c.WithEnvVariable("PATH", strings.Join(bins, ":")+":"+path), nil
}

// Diff shows what deploying the given source would change on the host.
//
// The new system is built in the host's own store, which already holds nearly
// all of it, and compared there with the running one by `nix store
// diff-closures`. The system's label names the commit, so it always differs.
func (h *NixosHost) Diff(
	ctx context.Context,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*"]
	source *dagger.Directory,
) (string, error) {
	c, err := h.container(source)
	if err != nil {
		return "", err
	}
	store := "ssh-ng://" + h.Name

	current, err := c.WithExec([]string{"ssh", h.Name, "readlink", "-f", "/run/current-system"}).Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("reading %s's current system: %w", h.Name, withExecOutput(err))
	}
	current = strings.TrimSpace(current)

	infof("building %s in %s's store", h.toplevel(), h.Name)
	built, err := c.WithExec(nixCommand("build", "--no-link", "--print-out-paths",
		"--store", store, "--eval-store", "auto", h.toplevel())).Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("building %s: %w", h.Name, withExecOutput(err))
	}
	built = strings.TrimSpace(built)

	if built == current {
		return fmt.Sprintf("%s: no changes, %s is running\n", h.Name, current), nil
	}
	diff, err := c.WithExec(nixCommand("store", "diff-closures", "--store", store, current, built)).Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("diffing %s's closures: %w", h.Name, withExecOutput(err))
	}
	return fmt.Sprintf("%s: %s -> %s\n\n%s", h.Name, current, built, diff), nil
}
