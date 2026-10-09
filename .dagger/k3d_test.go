package main

import (
	"fmt"
	"strings"
	"testing"
)

// The k3d workflow itself needs a Docker daemon and some minutes, so what is
// covered here is the part that decides whether a run is safe to start at all:
// which Docker endpoints can work, and whether a kubeconfig still points at the
// cluster the run created. Both are the difference between a failed test and a
// test that writes to the wrong cluster.

func TestDockerHostAddress(t *testing.T) {
	tests := []struct {
		name       string
		dockerHost string
		want       string
		wantErr    string
	}{
		{
			name:       "a routable tcp endpoint",
			dockerHost: "tcp://docker.example:2375",
			want:       "docker.example",
		},
		{
			name:       "a routable tcp endpoint by address",
			dockerHost: "tcp://10.1.2.3:2375",
			want:       "10.1.2.3",
		},
		{
			// The cluster's API port is published in the daemon's own network
			// namespace, and a socket names no host to reach it on.
			name:       "a unix socket",
			dockerHost: "unix:///var/run/docker.sock",
			wantErr:    "cannot be used",
		},
		{
			// Loopback inside a Dagger container is the container itself.
			name:       "loopback",
			dockerHost: "tcp://127.0.0.1:2375",
			wantErr:    "loopback",
		},
		{
			name:       "localhost",
			dockerHost: "tcp://localhost:2375",
			wantErr:    "loopback",
		},
		{
			name:       "a bare host with no scheme",
			dockerHost: "docker.example:2375",
			wantErr:    "cannot be used",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dockerHostAddress(tt.dockerHost)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("dockerHostAddress(%q): unexpected error: %v", tt.dockerHost, err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("dockerHostAddress(%q) = %q, want an error mentioning %q", tt.dockerHost, got, tt.wantErr)
			case tt.wantErr != "":
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("dockerHostAddress(%q) error %q, want it to mention %q", tt.dockerHost, err, tt.wantErr)
				}
				return
			}
			if got != tt.want {
				t.Errorf("dockerHostAddress(%q) = %q, want %q", tt.dockerHost, got, tt.want)
			}
		})
	}
}

func TestK3dClusterNameIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		name, err := k3dClusterName()
		if err != nil {
			t.Fatalf("k3dClusterName: %v", err)
		}
		if seen[name] {
			t.Fatalf("k3dClusterName returned %q twice", name)
		}
		seen[name] = true
	}
}

// k3d builds container and network names out of the cluster name, so it has to
// stay a DNS label with room left for k3d's own suffixes.
func TestK3dClusterNameIsDNSSafe(t *testing.T) {
	name, err := k3dClusterName()
	if err != nil {
		t.Fatalf("k3dClusterName: %v", err)
	}
	if len(name) > 32 {
		t.Errorf("cluster name %q is %d chars, want at most 32", name, len(name))
	}
	for _, r := range name {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789-", r) {
			t.Errorf("cluster name %q contains %q, which is not DNS-safe", name, r)
		}
	}
}

func TestCheckIdentity(t *testing.T) {
	cluster := &k3dCluster{Name: "homelab-abcdef", APIHost: dindAlias, KubeSystemUID: "1111-2222"}
	probe := func(server, context, uid string) string {
		return fmt.Sprintf("server=%s\ncontext=%s\nuid=%s\n", server, context, uid)
	}

	tests := []struct {
		name    string
		probe   string
		wantErr string
	}{
		{
			name:  "the cluster this run created",
			probe: probe(cluster.Server(), cluster.Context(), cluster.KubeSystemUID),
		},
		{
			name:    "another cluster at the same address",
			probe:   probe(cluster.Server(), cluster.Context(), "3333-4444"),
			wantErr: "kube-system namespace UID",
		},
		{
			// What a kubeconfig left over from a developer's shell looks like.
			name:    "a different API endpoint",
			probe:   probe("https://k8s.example:6443", "production", "3333-4444"),
			wantErr: "API endpoint",
		},
		{
			name:    "the right cluster under the wrong context",
			probe:   probe(cluster.Server(), "production", cluster.KubeSystemUID),
			wantErr: "context",
		},
		{
			// A field the probe never printed must not read as a match.
			name:    "a probe that did not print everything",
			probe:   "server=" + cluster.Server() + "\n",
			wantErr: "kube-system namespace UID",
		},
		{
			// kubectl warnings and blank lines are not key=value, so they are
			// ignored rather than shifting the parse.
			name: "noise around the values",
			probe: "W1009 warning: something\n\n" +
				probe(cluster.Server(), cluster.Context(), cluster.KubeSystemUID),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cluster.checkIdentity(tt.probe)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkIdentity: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkIdentity accepted %q, want an error mentioning %q", tt.probe, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("checkIdentity error %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// The isolation requirements are properties of the create invocation, so they
// are asserted on it rather than left to be noticed when a developer's
// kubeconfig changes under them.
func TestCreateArgsKeepTheCallersKubeconfigAlone(t *testing.T) {
	cluster := &k3dCluster{Name: "homelab-abcdef", APIHost: dindAlias}
	args := strings.Join(cluster.createArgs(), " ")

	for _, want := range []string{
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
		"--api-port 0.0.0.0:6445",
		"--tls-san=" + dindAlias + "@server:*",
		"--image " + k3sImage,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("createArgs is missing %q:\n%s", want, args)
		}
	}
}
