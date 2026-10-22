package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"dagger/homelab/internal/dagger"
)

func TestParseNixMachines(t *testing.T) {
	spec := `# /etc/nix/machines
ssh-ng://borg-0 x86_64-linux - 1 1 - - -
ssh-ng://borg-1 x86_64-linux /root/.ssh/id_ed25519 4 4
ssh://borg-2;ssh-ng://nix-ci@10.69.80.13 x86_64-linux - 2 1 big-parallel # trailing comment

borg-4 x86_64-linux
unix:///nix/var/nix/daemon-socket/socket`

	machines, warnings := parseNixMachines(spec)

	want := []string{
		"ssh-ng://borg-0 x86_64-linux - 1 1 - - -",
		"ssh-ng://borg-1 x86_64-linux - 4 4",
		"ssh://borg-2",
		"ssh-ng://nix-ci@10.69.80.13 x86_64-linux - 2 1 big-parallel",
	}
	if !slices.Equal(machines, want) {
		t.Errorf("machines:\n got %q\nwant %q", machines, want)
	}
	for _, mention := range []string{"/root/.ssh/id_ed25519", "borg-4", "unix://"} {
		if !slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, mention) }) {
			t.Errorf("no warning mentions %q: %q", mention, warnings)
		}
	}
}

func TestParseNixProbes(t *testing.T) {
	out := `trusted ssh-ng://borg-1 x86_64-linux - 4 4
untrusted ssh-ng://nix-ci@10.69.80.12 x86_64-linux - 4 4
unreachable ssh-ng://borg-0 x86_64-linux - 1 1: ssh: connect to host borg-0.msng.to port 22: Connection timed out
`
	probes := parseNixProbes(out)
	if len(probes) != 3 {
		t.Fatalf("got %d probes, want 3: %+v", len(probes), probes)
	}
	if probes[2].machine != "ssh-ng://borg-0 x86_64-linux - 1 1" {
		t.Errorf("unreachable machine = %q", probes[2].machine)
	}
	if !strings.Contains(probes[2].detail, "Connection timed out") {
		t.Errorf("unreachable detail = %q", probes[2].detail)
	}

	remote := newRemoteNix(nil, probes)
	if remote == nil || len(remote.machines) != 2 || remote.trusted {
		t.Errorf("one untrusted builder among two: got %+v, want 2 machines, untrusted", remote)
	}
	if remote := newRemoteNix(nil, probes[:1]); remote == nil || !remote.trusted {
		t.Errorf("a trusted builder alone: got %+v, want trusted", remote)
	}
	if remote := newRemoteNix(nil, probes[2:]); remote != nil {
		t.Errorf("no builder answered: got %+v, want nil", remote)
	}
}

func TestRemoteNixFailure(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		remote bool
	}{
		{"error: cannot build on 'ssh-ng://borg-1': error: failed to start SSH connection to 'borg-1'", true},
		{"error: Unable to start any build; either increase '--max-jobs' or enable remote builds.", true},
		{"error: cannot add path '/nix/store/abc-x' because it lacks a signature by a trusted key", true},
		{"Host key verification failed.", true},
		{"error: build of '/nix/store/abc.drv' on 'ssh-ng://borg-1' failed: builder failed with exit code 2", false},
		{"error: undefined variable 'pkgs'", false},
	} {
		err := fmt.Errorf("exec: %w", &dagger.ExecError{Stderr: tc.stderr})
		if got := remoteNixFailure(err) != ""; got != tc.remote {
			t.Errorf("remoteNixFailure(%q) = %v, want %v", tc.stderr, got, tc.remote)
		}
	}
	if remoteNixFailure(errors.New("Connection refused")) != "" {
		t.Error("a failure that is not an exec's must never be retried")
	}
}

func TestNixBuildersSSHConfig(t *testing.T) {
	config := nixBuildersSSHConfig(true, "tkennedy")
	overrides := strings.Index(config, "IdentitiesOnly no")
	include := strings.Index(config, "Include ")
	user := strings.Index(config, "User tkennedy")
	if overrides < 0 || include < 0 || user < 0 || (overrides >= include || include >= user) {
		t.Errorf("want overrides, then the Include, then defaults:\n%s", config)
	}
	if strings.Contains(nixBuildersSSHConfig(false, ""), "Include") {
		t.Error("no ssh_config to include, but the config includes one")
	}
}

func TestNixSystem(t *testing.T) {
	for platform, want := range map[dagger.Platform]string{
		"linux/amd64":    "x86_64-linux",
		"linux/arm64/v8": "aarch64-linux",
		"linux/arm64":    "aarch64-linux",
		"linux/s390x":    "",
		"darwin/arm64":   "",
	} {
		if got := nixSystem(platform); got != want {
			t.Errorf("nixSystem(%q) = %q, want %q", platform, got, want)
		}
	}
}

func TestNixMachineBuilds(t *testing.T) {
	for _, tc := range []struct {
		machine string
		builds  bool
	}{
		{"ssh-ng://borg-1 x86_64-linux - 4 4", false},
		{"ssh-ng://mac aarch64-linux,x86_64-linux - 4 4", true},
		{"ssh-ng://mac aarch64-linux", true},
		{"ssh-ng://anything", true},
		{"ssh-ng://anything - - 1", true},
	} {
		if got := nixMachineBuilds(tc.machine, "aarch64-linux"); got != tc.builds {
			t.Errorf("nixMachineBuilds(%q, aarch64-linux) = %v, want %v", tc.machine, got, tc.builds)
		}
	}
}
