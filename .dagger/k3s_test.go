package main

import (
	"fmt"
	"strings"
	"testing"
)

// The k3s workflow itself needs a privileged engine and some minutes, so what is
// covered here is the part that decides whether a run is safe: whether a
// kubeconfig still points at the cluster the run created. That is the difference
// between a failed test and a test that writes to the wrong cluster.

func TestK3sClusterNameIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		name, err := k3sClusterName()
		if err != nil {
			t.Fatalf("k3sClusterName: %v", err)
		}
		if seen[name] {
			t.Fatalf("k3sClusterName returned %q twice", name)
		}
		seen[name] = true
	}
}

// The name ends up in a kubeconfig context, so it stays a short DNS label.
func TestK3sClusterNameIsDNSSafe(t *testing.T) {
	name, err := k3sClusterName()
	if err != nil {
		t.Fatalf("k3sClusterName: %v", err)
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
	cluster := &k3sCluster{Name: "homelab-abcdef", APIHost: k3sAlias, KubeSystemUID: "1111-2222"}
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

// The API server's credential and certificate are properties of the server
// invocation, so they are asserted on it rather than discovered when a client
// fails to verify.
func TestServerArgs(t *testing.T) {
	cluster := &k3sCluster{Name: "homelab-abcdef", APIHost: k3sAlias}
	args := strings.Join(cluster.serverArgs(), " ")

	for _, want := range []string{
		"--tls-san " + k3sAlias,
		"--kube-apiserver-arg token-auth-file=" + apiAuthFilePath,
		"--disable traefik",
		"--disable metrics-server",
		"--egress-selector-mode=disabled",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("serverArgs is missing %q:\n%s", want, args)
		}
	}
}

// k3s reads this file with encoding/csv, so the group list has to be one quoted
// field.
func TestTokenAuthLine(t *testing.T) {
	got := tokenAuthLine("abc123")
	want := "abc123,homelab-admin,homelab-admin,\"system:masters\"\n"
	if got != want {
		t.Errorf("tokenAuthLine = %q, want %q", got, want)
	}
}
