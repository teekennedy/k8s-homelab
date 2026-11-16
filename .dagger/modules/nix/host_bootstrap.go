// Bootstraps a new NixOS host's repo files and installs it with nixos-anywhere.

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"dagger/nix/internal/dagger"
)

//go:embed scripts/host-secrets.sh
var hostSecretsScript string

//go:embed scripts/host-install.sh
var hostInstallScript string

// hostTools are the packages bootstrap and install run, beyond the Nix image's.
var hostTools = []string{"sops", "gnupg", "ssh-to-age", "mkpasswd", "yq-go", "nixos-anywhere"}

// hostWork is a tmpfs for plaintext secrets, which never reach a layer.
const hostWork = "/run/homelab-host"

// installerSSHOptions accept an installer's host key: it is generated on
// every boot of the ISO.
var installerSSHOptions = []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"}

func (h *NixosHost) dir() string {
	return path.Join("nix/hosts", h.Name)
}

// withSops gives c the age key sops decrypts with and a GnuPG keyring holding
// the committed PGP public keys, which sops encrypts to. The keyring trusts
// them outright: they are the repo's own.
func withSops(c *dagger.Container, sopsAgeKey *dagger.Secret) *dagger.Container {
	return c.
		WithSecretVariable("SOPS_AGE_KEY", sopsAgeKey).
		WithEnvVariable("GNUPGHOME", "/root/.gnupg").
		WithExec([]string{"sh", "-euc", `
install -d -m 700 "$GNUPGHOME"
echo "trust-model always" >"$GNUPGHOME/gpg.conf"
gpg --batch --quiet --import .sops/pgp/*.asc
`})
}

// Bootstraps a new host's files: its secrets, its .sops.yaml entries and its
// hardware report.
//
// Returns the changes to apply to the repo; run it with --auto-apply from the
// repo root. It never touches the target's disks. Every step keeps what the
// repo already has, so a configured host comes back with no changes.
//
//  1. Generates whatever nix/hosts/<name>/secrets.yaml lacks (SSH host key
//     pair, the default user's password hash, the restic password) and
//     writes it encrypted.
//  2. Adds the host's age key, its secrets' creation rule and its access to
//     the nix/modules secrets to .sops.yaml, re-keying those secrets.
//  3. Generates nix/hosts/<name>/facter.json from the installer at ip.
//  4. Builds the host's system once disko.devices.disk.main.device is set,
//     and otherwise lists the disks to choose it from.
//
// The host must be in the flake's nixosConfigurations, and the target booted
// from the installer ISO, which accepts the forwarded agent's keys as root.
func (h *NixosHost) Bootstrap(
	ctx context.Context,
	// The installer's address.
	ip string,
	// The age key sops decrypts with, such as
	// file:~/.config/sops/age/keys.txt. It must be one of the common keys
	// .sops.yaml encrypts host and module secrets to.
	sopsAgeKey *dagger.Secret,
	// The password for the host's default user. Needed only when
	// secrets.yaml has no hash for it yet.
	// +optional
	password *dagger.Secret,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!config/gen/production/env.json", "!.sops.yaml", "!.sops/**"]
	source *dagger.Directory,
) (*dagger.Changeset, error) {
	c, err := h.container(source)
	if err != nil {
		return nil, err
	}
	if err := h.checkDefined(ctx, c); err != nil {
		return nil, err
	}
	if c, err = withTools(ctx, c, hostTools...); err != nil {
		return nil, err
	}
	c = withSops(c, sopsAgeKey)

	steps := []func(context.Context, *dagger.Container) (*dagger.Container, error){
		func(ctx context.Context, c *dagger.Container) (*dagger.Container, error) {
			return h.ensureDefaultNix(ctx, c, source)
		},
		func(ctx context.Context, c *dagger.Container) (*dagger.Container, error) {
			return h.ensureSecrets(ctx, c, password)
		},
		func(ctx context.Context, c *dagger.Container) (*dagger.Container, error) {
			return h.ensureFacter(ctx, c, source, ip)
		},
	}
	for _, step := range steps {
		if c, err = step(ctx, c); err != nil {
			return nil, err
		}
	}
	if c, err = c.Sync(ctx); err != nil {
		return nil, fmt.Errorf("bootstrapping %s: %w", h.Name, withExecOutput(err))
	}

	bootstrapped := c.Directory("/src")
	if err := h.buildWhenDiskSet(ctx, c, bootstrapped); err != nil {
		return nil, err
	}
	return bootstrapped.Changes(source), nil
}

// checkDefined fails unless the host is in the flake's nixosConfigurations.
func (h *NixosHost) checkDefined(ctx context.Context, c *dagger.Container) error {
	defined, err := c.WithExec(nixCommand("eval", ".#nixosConfigurations",
		"--apply", fmt.Sprintf("x: builtins.hasAttr %q x", h.Name), "--json")).Stdout(ctx)
	if err != nil {
		return fmt.Errorf("evaluating the flake: %w", withExecOutput(err))
	}
	if strings.TrimSpace(defined) != "true" {
		return fmt.Errorf("%s is not in the flake's nixosConfigurations: add it to borgHosts in flake.nix first", h.Name)
	}
	return nil
}

// ensureDefaultNix creates the host's default.nix, which the flake imports.
func (h *NixosHost) ensureDefaultNix(ctx context.Context, c *dagger.Container, source *dagger.Directory) (*dagger.Container, error) {
	file := path.Join(h.dir(), "default.nix")
	ok, err := source.Exists(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("looking for %s: %w", file, err)
	}
	if ok {
		return c, nil
	}
	infof("creating %s", file)
	return c.WithNewFile(path.Join("/src", file), "{...}: {}\n"), nil
}

// ensureSecrets fills in the host's secrets.yaml, then gives the host its
// .sops.yaml entries and re-keys the module secrets when they change.
func (h *NixosHost) ensureSecrets(ctx context.Context, c *dagger.Container, password *dagger.Secret) (*dagger.Container, error) {
	const sopsPath = "/src/.sops.yaml"
	data, err := c.File(sopsPath).Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading .sops.yaml: %w", err)
	}
	sops, err := parseSopsConfig([]byte(data))
	if err != nil {
		return nil, err
	}
	pgp, age, err := sops.CommonKeys()
	if err != nil {
		return nil, err
	}

	if password != nil {
		c = c.WithSecretVariable("HOST_PASSWORD", password)
	}
	c, err = c.
		WithMountedTemp(hostWork).
		WithEnvVariable("HOST", h.Name).
		WithEnvVariable("SECRETS", path.Join(h.dir(), "secrets.yaml")).
		WithEnvVariable("WORK", hostWork).
		WithEnvVariable("PUBKEY_OUT", "/tmp/ssh_host_public_key").
		WithEnvVariable("AGE_RECIPIENTS", strings.Join(age, ",")).
		WithEnvVariable("PGP_RECIPIENTS", strings.Join(pgp, ",")).
		WithNewFile("/host-secrets.sh", hostSecretsScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithExec([]string{"/host-secrets.sh"}).
		WithoutSecretVariable("HOST_PASSWORD").
		Sync(ctx)
	if err != nil {
		return nil, fmt.Errorf("generating %s's secrets: %w", h.Name, withExecOutput(err))
	}

	return h.updateSops(ctx, c, sops)
}

// updateSops gives the host its .sops.yaml entries, keyed to the SSH public
// key ensureSecrets left at /tmp/ssh_host_public_key, and re-keys the module
// secrets when their rule changes.
func (h *NixosHost) updateSops(ctx context.Context, c *dagger.Container, sops *sopsConfig) (*dagger.Container, error) {
	const sopsPath = "/src/.sops.yaml"
	ageKey, err := c.WithExec([]string{"sh", "-c", "ssh-to-age < /tmp/ssh_host_public_key"}).Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("converting the host key to age: %w", withExecOutput(err))
	}
	edits, err := sops.ApplyHost(h.Name, strings.TrimSpace(ageKey))
	if err != nil {
		return nil, err
	}
	if edits.Any() {
		out, err := sops.Bytes()
		if err != nil {
			return nil, err
		}
		infof("updating .sops.yaml for %s", h.Name)
		c = c.WithNewFile(sopsPath, string(out))
	}
	if edits.ModulesRekey() {
		// updatekeys leaves a file whose keys already match the rule alone.
		c = c.WithExec([]string{"sh", "-euc", `
for f in nix/modules/*/*.enc.yaml; do
  sops updatekeys --yes "$f"
done
`})
	}
	return c, nil
}

// ensureFacter generates the host's facter.json from the installer at ip.
func (h *NixosHost) ensureFacter(ctx context.Context, c *dagger.Container, source *dagger.Directory, ip string) (*dagger.Container, error) {
	facter := path.Join(h.dir(), "facter.json")
	ok, err := source.Exists(ctx, facter)
	if err != nil {
		return nil, fmt.Errorf("looking for %s: %w", facter, err)
	}
	if ok {
		return c, nil
	}
	infof("generating %s from the installer at %s", facter, ip)
	args := []string{
		"nixos-anywhere", "--flake", ".#" + h.Name,
		"--generate-hardware-config", "nixos-facter", facter,
		"--phases", "", "--target-host", "root@" + ip,
	}
	for _, o := range installerSSHOptions {
		args = append(args, "--ssh-option", o)
	}
	return c.WithExec(args), nil
}

// buildWhenDiskSet builds the host's system if its disko main disk is set,
// and otherwise lists the disks facter.json found. A build failure fails the
// bootstrap; an unset disk does not, so that its changes can be applied
// before choosing one.
func (h *NixosHost) buildWhenDiskSet(ctx context.Context, c *dagger.Container, source *dagger.Directory) error {
	device, err := c.WithExec(nixCommand("eval", "--raw",
		".#nixosConfigurations."+h.Name+".config.disko.devices.disk.main.device"),
		dagger.ContainerWithExecOpts{Expect: dagger.ReturnTypeAny}).Stdout(ctx)
	if err != nil {
		return fmt.Errorf("evaluating %s's disko disk: %w", h.Name, err)
	}
	if device = strings.TrimSpace(device); device == "" {
		disks, err := facterDisks(ctx, c.File(path.Join("/src", h.dir(), "facter.json")))
		if err != nil {
			return err
		}
		warnf("disko.devices.disk.main.device is not set for %s. Apply these changes, set it in flake.nix to one of these, and run bootstrap again:\n  %s",
			h.Name, strings.Join(disks, "\n  "))
		return nil
	}
	infof("disko main disk: %s", device)

	built, err := h.Remote.build(ctx, nixImageStore, func(env nixEnv) *dagger.Container {
		args := nixCommand("build", "--no-link", "--print-out-paths", h.toplevel())
		if env.Remote {
			args = append(args, env.StoreOptions...)
		}
		return env.Container.WithDirectory("/src", source).WithWorkdir("/src").WithExec(env.Exec(args))
	})
	if err == nil {
		_, err = built.Sync(ctx)
	}
	if err != nil {
		return fmt.Errorf("building %s: %w", h.Name, withExecOutput(err))
	}
	return nil
}

// facterDisks lists the /dev/disk/by-id names of the disks in a facter report.
func facterDisks(ctx context.Context, report *dagger.File) ([]string, error) {
	data, err := report.Contents(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading facter.json: %w", err)
	}
	var facter struct {
		Hardware struct {
			Disk []struct {
				UnixDeviceNames []string `json:"unix_device_names"`
			} `json:"disk"`
		} `json:"hardware"`
	}
	if err := json.Unmarshal([]byte(data), &facter); err != nil {
		return nil, fmt.Errorf("parsing facter.json: %w", err)
	}
	var disks []string
	for _, disk := range facter.Hardware.Disk {
		for _, name := range disk.UnixDeviceNames {
			if strings.Contains(name, "/disk/by-id/") && !slices.Contains(disks, name) {
				disks = append(disks, name)
			}
		}
	}
	return disks, nil
}

// Installs NixOS on a bootstrapped host with nixos-anywhere. THIS ERASES THE
// TARGET'S DISKS.
//
// Run bootstrap first and apply its changes. The host's SSH key from
// secrets.yaml is installed at /persistent/etc/ssh, so the new system can
// decrypt its secrets. The target reboots into NixOS when done.
func (h *NixosHost) Install(
	ctx context.Context,
	// The installer's address.
	ip string,
	// The age key sops decrypts secrets.yaml with.
	sopsAgeKey *dagger.Secret,
	// +defaultPath="/"
	// +ignore=["*", "!flake.nix", "!flake.lock", "!nix/**/*", "!config/gen/production/env.json"]
	source *dagger.Directory,
) (string, error) {
	c, err := h.container(source)
	if err != nil {
		return "", err
	}
	if ok, err := source.Exists(ctx, path.Join(h.dir(), "secrets.yaml")); err != nil {
		return "", fmt.Errorf("looking for secrets.yaml: %w", err)
	} else if !ok {
		return "", fmt.Errorf("%s/secrets.yaml does not exist: run bootstrap first", h.dir())
	}
	c, err = withTools(ctx, c, "sops", "yq-go", "nixos-anywhere")
	if err != nil {
		return "", err
	}
	out, err := c.
		WithSecretVariable("SOPS_AGE_KEY", sopsAgeKey).
		WithMountedTemp(hostWork).
		WithEnvVariable("HOST", h.Name).
		WithEnvVariable("SECRETS", path.Join(h.dir(), "secrets.yaml")).
		WithEnvVariable("WORK", hostWork).
		WithEnvVariable("TARGET", "root@"+ip).
		WithEnvVariable("SSH_OPTIONS", strings.Join(installerSSHOptions, " ")).
		WithNewFile("/host-install.sh", hostInstallScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithExec([]string{"/host-install.sh"}).
		Stdout(ctx)
	if err != nil {
		return "", fmt.Errorf("installing %s: %w", h.Name, withExecOutput(err))
	}
	return out, nil
}
