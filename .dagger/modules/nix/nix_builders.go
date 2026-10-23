// Remote Nix builders for the module's Nix and devenv builds, reached over
// ssh-ng through a forwarded SSH agent, with a fallback to building in the
// container when they cannot be used.

package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dagger/nix/internal/dagger"
)

//go:embed scripts/nix-builders-probe.sh
var nixBuildersProbeScript string

//go:embed scripts/nix-builders-exec.sh
var nixBuildersExecScript string

const (
	// nixBuildersDir holds everything a remote build reads: the machines file,
	// known_hosts, the caller's ssh_config and the ssh client's binary cache.
	nixBuildersDir = "/etc/homelab-nix"
	// nixBuildersAgent is where the forwarded SSH agent is mounted.
	nixBuildersAgent = "/run/homelab-nix/ssh-agent.sock"
	// nixBuildersExec puts ssh on PATH, then runs its arguments.
	nixBuildersExec = "/usr/local/bin/homelab-nix-exec"
	// nixBuildersProbeWindow is how long a probe's answer is reused. Every
	// build in a `dagger check` shares it; a builder that goes down inside the
	// window costs a failed attempt and a local rebuild.
	nixBuildersProbeWindow = 5 * time.Minute
)

// Builders hands the module's Nix and devenv builds to remote builders.
//
// A build uses them when the machines are set, the SSH agent is forwarded,
// and at least one machine answers a probe. Otherwise, or when the remote
// attempt fails for a reason that is not the build's own, it builds in the
// container as it does without builders.
type Builders struct {
	// Machines in the format of Nix's `builders` setting, normalised by
	// parseNixMachines.
	// +private
	Machines []string
	// +private
	SSHAuthSock *dagger.Socket
	// +private
	SSHConfig *dagger.File
	// +private
	SSHUser string
	// +private
	KnownHosts *dagger.File
	// Problems with the configured machines, reported with each probe.
	// +private
	Warnings []string
}

// newBuilders returns the builders for a constructor's settings.
func newBuilders(
	machines string,
	sshAuthSock *dagger.Socket,
	sshConfig *dagger.File,
	sshUser string,
	knownHosts *dagger.File,
) *Builders {
	parsed, warnings := parseNixMachines(machines)
	return &Builders{
		Machines:    parsed,
		SSHAuthSock: sshAuthSock,
		SSHConfig:   sshConfig,
		SSHUser:     sshUser,
		KnownHosts:  knownHosts,
		Warnings:    warnings,
	}
}

// configured reports whether there is anything to probe.
func (b *Builders) configured() bool {
	return b != nil && len(b.Machines) > 0 && b.SSHAuthSock != nil
}

// Status probes every configured builder and reports which ones a build
// would use. It always probes afresh, unlike a build, which reuses a probe
// for a few minutes.
func (b *Builders) Status(ctx context.Context) (string, error) {
	var out strings.Builder
	for _, warning := range b.Warnings {
		fmt.Fprintf(&out, "warning: %s\n", warning)
	}
	switch {
	case len(b.Machines) == 0:
		out.WriteString("no builders configured: set the nixBuilders setting\n")
		return out.String(), nil
	case b.SSHAuthSock == nil:
		out.WriteString("no SSH agent forwarded: set the sshAuthSock setting\n")
		return out.String(), nil
	}
	machines, system, err := b.forEngine(ctx)
	if err != nil {
		return "", err
	}
	for _, skipped := range nixMachinesExcept(b.Machines, machines) {
		fmt.Fprintf(&out, "%-11s %s: does not build %s, the engine's system\n", "skipped", nixMachineURI(skipped), system)
	}
	if len(machines) == 0 {
		fmt.Fprintf(&out, "no builder builds %s: builds run in the container\n", system)
		return out.String(), nil
	}
	probes, err := b.probe(ctx, machines, time.Now().Format(time.RFC3339Nano))
	if err != nil {
		return "", err
	}
	for _, p := range probes {
		fmt.Fprintf(&out, "%-11s %s\n", p.status, p.detail)
	}
	if remote := newRemoteNix(b, probes); remote == nil {
		out.WriteString("no builder answered: builds run in the container\n")
	} else {
		fmt.Fprintf(&out, "builds use %d of %d builders, %s\n", len(remote.machines), len(b.Machines), remote.mode())
	}
	return out.String(), nil
}

// nixStore returns a Nix container whose /nix is one of two stores. The
// signed-only store holds nothing built in the container, so that a builder
// that does not trust the container accepts every path in it as an input;
// the other store holds everything else.
type nixStore func(signedOnly bool) *dagger.Container

// nixEnv is a container prepared for one way of building, and how to run Nix
// in it.
type nixEnv struct {
	Container *dagger.Container
	Remote    bool
}

// Exec returns args as an exec that can reach the builders.
func (e nixEnv) Exec(args []string) []string {
	if !e.Remote {
		return args
	}
	return append([]string{nixBuildersExec}, args...)
}

// build runs steps against the builders when they can be used, and in the
// container otherwise.
//
// The remote attempt is synced here, so that a failure that is the builders'
// and not the build's can be retried locally; a container built without
// builders stays lazy, exactly as it was before builders existed.
func (b *Builders) build(ctx context.Context, store nixStore, steps func(nixEnv) *dagger.Container) (*dagger.Container, error) {
	local := func() *dagger.Container {
		return steps(nixEnv{Container: store(false)})
	}
	if !b.configured() {
		return local(), nil
	}
	remote, err := b.remote(ctx)
	if err != nil {
		warnf("building without remote builders: %v", err)
		return local(), nil
	}
	if remote == nil {
		return local(), nil
	}

	built, err := steps(nixEnv{Container: remote.configure(store(!remote.trusted)), Remote: true}).Sync(ctx)
	if err == nil {
		return built, nil
	}
	if reason := remoteNixFailure(err); reason != "" {
		warnf("remote Nix build failed (%s); building in the container instead", reason)
		return local(), nil
	}
	return nil, fmt.Errorf("building with remote Nix builders: %w", err)
}

// remoteNix is the builders a probe found usable.
type remoteNix struct {
	builders *Builders
	machines []string
	// trusted is whether every one of them trusts the container, which can
	// then build locally too and hand them unsigned paths.
	trusted bool
}

// newRemoteNix returns the usable machines among probes, or nil when there
// are none.
func newRemoteNix(b *Builders, probes []nixProbe) *remoteNix {
	remote := &remoteNix{builders: b, trusted: true}
	for _, p := range probes {
		if p.status == "unreachable" {
			continue
		}
		remote.machines = append(remote.machines, p.machine)
		if p.status != "trusted" {
			remote.trusted = false
		}
	}
	if len(remote.machines) == 0 {
		return nil
	}
	return remote
}

func (r *remoteNix) mode() string {
	if r.trusted {
		return "alongside the container"
	}
	return "instead of the container, which builds nothing itself since they do not trust it"
}

// remote probes the builders and returns the ones that answered, or nil when
// none did.
func (b *Builders) remote(ctx context.Context) (*remoteNix, error) {
	for _, warning := range b.Warnings {
		warnf("nix builders: %s", warning)
	}
	machines, system, err := b.forEngine(ctx)
	if err != nil {
		return nil, err
	}
	if len(machines) == 0 {
		infof("building in the container: no remote Nix builder builds %s, the engine's system", system)
		return nil, nil
	}
	window := time.Now().Truncate(nixBuildersProbeWindow).Format(time.RFC3339)
	probes, err := b.probe(ctx, machines, window)
	if err != nil {
		return nil, err
	}
	remote := newRemoteNix(b, probes)
	if remote == nil {
		infof("building in the container: no remote Nix builder answered (`dagger api call nix builders status` for details)")
		return nil, nil
	}
	names := make([]string, len(remote.machines))
	for i, machine := range remote.machines {
		names[i] = nixMachineURI(machine)
	}
	infof("building with remote Nix builders %s, %s", strings.Join(names, ", "), remote.mode())
	return remote, nil
}

// nixProbe is one machine's answer to the probe.
type nixProbe struct {
	// status is trusted, untrusted, unknown or unreachable.
	status  string
	machine string
	detail  string
}

// forEngine returns the machines that build for the engine's platform, which
// is what every build in the module targets, and that platform's Nix system.
// The others could only ever decline: an Apple-silicon engine's builds are
// aarch64-linux, which an x86_64-linux builder cannot build.
func (b *Builders) forEngine(ctx context.Context) ([]string, string, error) {
	platform, err := dag.DefaultPlatform(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("reading the engine's platform: %w", err)
	}
	system := nixSystem(platform)
	if system == "" {
		return b.Machines, string(platform), nil
	}
	var machines []string
	for _, machine := range b.Machines {
		if nixMachineBuilds(machine, system) {
			machines = append(machines, machine)
		}
	}
	return machines, system, nil
}

// nixSystem is the Nix system a Linux platform builds for, or "" for one this
// module does not know, whose machines are then all kept.
func nixSystem(platform dagger.Platform) string {
	osArch := strings.Split(string(platform), "/")
	if len(osArch) < 2 || osArch[0] != "linux" {
		return ""
	}
	arch, ok := map[string]string{
		"amd64":   "x86_64",
		"arm64":   "aarch64",
		"386":     "i686",
		"riscv64": "riscv64",
	}[osArch[1]]
	if !ok {
		return ""
	}
	return arch + "-linux"
}

// nixMachineBuilds reports whether a machine line lists system. A line with no
// systems field, or "-" in it, builds for whatever the client is, as in Nix.
func nixMachineBuilds(machine, system string) bool {
	fields := strings.Fields(machine)
	if len(fields) < 2 || fields[1] == "-" {
		return true
	}
	for s := range strings.SplitSeq(fields[1], ",") {
		if s == system {
			return true
		}
	}
	return false
}

// nixMachinesExcept returns the machines in all that are not in kept.
func nixMachinesExcept(all, kept []string) []string {
	var rest []string
	for _, machine := range all {
		if !slices.Contains(kept, machine) {
			rest = append(rest, machine)
		}
	}
	return rest
}

// probe asks each machine's daemon for its store info. window keys the
// probe's cache: the same window reuses an earlier answer.
func (b *Builders) probe(ctx context.Context, machines []string, window string) ([]nixProbe, error) {
	out, err := b.withSSH(nixImage()).
		WithNewFile(nixBuildersDir+"/candidates", strings.Join(machines, "\n")+"\n").
		WithNewFile("/probe.sh", nixBuildersProbeScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithEnvVariable("HOMELAB_NIX_PROBE_WINDOW", window).
		WithExec([]string{"/probe.sh"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("probing the builders: %w", withExecOutput(err))
	}
	return parseNixProbes(out), nil
}

// parseNixProbes reads the probe script's output: "<status> <machine>", with
// the reason after ": " for an unreachable one.
func parseNixProbes(out string) []nixProbe {
	var probes []nixProbe
	for line := range strings.SplitSeq(out, "\n") {
		status, rest, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		p := nixProbe{status: status, machine: rest, detail: nixMachineURI(rest)}
		if status == "unreachable" {
			machine, reason, _ := strings.Cut(rest, ": ")
			p.machine = machine
			p.detail = nixMachineURI(machine) + ": " + reason
		}
		probes = append(probes, p)
	}
	return probes
}

// withSSH gives c an ssh client setup that reaches the builders through the
// forwarded agent and nothing else of the caller's.
func (b *Builders) withSSH(c *dagger.Container) *dagger.Container {
	c = c.
		WithNewFile("/etc/ssh/ssh_config", nixBuildersSSHConfig(b.SSHConfig != nil, b.SSHUser)).
		WithUnixSocket(nixBuildersAgent, b.SSHAuthSock).
		WithEnvVariable("SSH_AUTH_SOCK", nixBuildersAgent)
	if b.KnownHosts != nil {
		c = c.WithFile(nixBuildersDir+"/known_hosts", b.KnownHosts)
	}
	if b.SSHConfig != nil {
		c = c.WithFile(nixBuildersDir+"/user_ssh_config", b.SSHConfig)
	}
	return c
}

// configure prepares c to build on the probed machines.
//
// A builder that does not trust the container, as in CI, takes only signed
// or content-addressed paths as inputs. So with such a builder max-jobs is 0
// and c must be on the signed-only store: nothing is built in the container,
// every path in it came from cache.nixos.org or a builder, and both sign
// what they hand out.
//
// The builders are not substituters as well. Asking each of them about every
// path made evaluating the lab CLI take 10 seconds rather than 1, and a
// builder hands back a path it already has when it is asked to build it.
func (r *remoteNix) configure(c *dagger.Container) *dagger.Container {
	config := []string{
		"builders = @" + nixBuildersDir + "/machines",
		// A builder fetches what it lacks itself, from its own caches and
		// peers, instead of the container uploading it.
		"builders-use-substitutes = true",
		"connect-timeout = 5",
	}
	if !r.trusted {
		config = append(config, "max-jobs = 0")
	}

	return r.builders.withSSH(c).
		WithNewFile(nixBuildersDir+"/machines", strings.Join(r.machines, "\n")+"\n").
		WithMountedDirectory(nixBuildersDir+"/ssh-client", sshClientCache()).
		WithNewFile(nixBuildersExec, nixBuildersExecScript, dagger.ContainerWithNewFileOpts{Permissions: 0o755}).
		WithEnvVariable("NIX_CONFIG", strings.Join(config, "\n"))
}

// sshClientCache is a binary cache holding the Nix image's ssh client, which
// the exec wrapper copies into a store that has none, such as devenv's.
func sshClientCache() *dagger.Directory {
	return nixImage().
		WithExec([]string{"sh", "-euc", `
ssh=$(readlink -f "$(command -v ssh)")
pkg=${ssh%/bin/ssh}
nix --extra-experimental-features nix-command copy --to file:///out/cache "$pkg"
echo "$pkg" > /out/path
`}).
		Directory("/out")
}

// nixBuildersSSHConfig is the ssh_config remote builds run with.
//
// ssh keeps the first value it reads for an option, so the block before the
// caller's config overrides it and the block after only fills in defaults. The
// overrides are what the container needs: no prompts, the module's own
// known_hosts, keys from the agent alone, since the IdentityFile paths the
// caller's config names do not exist here, and a control socket the container
// can create.
//
// Connection sharing matters: Nix hands each derivation to a builder from a
// process of its own, which opens a new connection unless a master is already
// running. Sharing one made six small builds on one builder take 3.2s
// instead of 8.9s.
func nixBuildersSSHConfig(include bool, user string) string {
	var config strings.Builder
	config.WriteString(`Host *
  BatchMode yes
  IdentitiesOnly no
  StrictHostKeyChecking yes
  UserKnownHostsFile ` + nixBuildersDir + `/known_hosts
  ControlMaster auto
  ControlPath /tmp/homelab-nix-ssh-%C
  ControlPersist 60
  ConnectTimeout 5
  ServerAliveInterval 15
  ServerAliveCountMax 4
`)
	if include {
		config.WriteString("Include " + nixBuildersDir + "/user_ssh_config\n")
	}
	if user != "" {
		config.WriteString("Host *\n  User " + user + "\n")
	}
	return config.String()
}

// parseNixMachines normalises a `builders` value: one machine per line or per
// ";", with "#" comments. Each machine's SSH key is replaced with "-", since
// the key is the agent's; a machine whose store is not reached over SSH is
// dropped, since the agent is all the container can authenticate with.
func parseNixMachines(spec string) (machines, warnings []string) {
	for line := range strings.SplitSeq(strings.ReplaceAll(spec, ";", "\n"), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		uri := fields[0]
		if !strings.HasPrefix(uri, "ssh-ng://") && !strings.HasPrefix(uri, "ssh://") {
			warnings = append(warnings, fmt.Sprintf("ignoring builder %s: only ssh-ng:// and ssh:// builders are supported", uri))
			continue
		}
		if len(fields) > 2 && fields[2] != "-" {
			warnings = append(warnings, fmt.Sprintf("builder %s: ignoring SSH key %s; keys come from the forwarded agent", uri, fields[2]))
			fields[2] = "-"
		}
		machines = append(machines, strings.Join(fields, " "))
	}
	return machines, warnings
}

// nixMachineURI is a machine line's store URI.
func nixMachineURI(machine string) string {
	uri, _, _ := strings.Cut(machine, " ")
	return uri
}

// remoteNixFailures are what Nix and ssh print when a remote build failed for
// want of a builder, rather than because the build itself is broken.
var remoteNixFailures = []string{
	"cannot build on '",
	"failed to start SSH",
	"cannot open connection to remote store",
	"Failed to find a machine for remote build",
	"Unable to start any build",
	"lacks a signature by a trusted key",
	"you are not privileged",
	"unable to execute 'ssh'",
	"Host key verification failed",
	"Permission denied (publickey",
	"Connection refused",
	"Connection timed out",
	"Connection reset",
	"Connection closed by",
	"No route to host",
	"Could not resolve hostname",
	"Broken pipe",
}

// remoteNixFailure returns the first of remoteNixFailures in err's output, or
// "" when there is none and the failure is the build's own.
func remoteNixFailure(err error) string {
	var execErr *dagger.ExecError
	if !errors.As(err, &execErr) {
		return ""
	}
	output := execErr.Stdout + "\n" + execErr.Stderr
	for _, failure := range remoteNixFailures {
		if strings.Contains(output, failure) {
			return strings.TrimSpace(strings.TrimSuffix(failure, "'"))
		}
	}
	return ""
}
