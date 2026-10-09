package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"dagger/homelab/internal/dagger"
)

// Everything here is about one throwaway k3s cluster: starting it, reaching it
// from a Dagger container, proving a kubeconfig still points at it, and taking
// it down again. Nothing in this file knows what gets deployed to it.
//
// The cluster is a single `k3s server` run as a Dagger service, so it needs an
// engine that allows privileged execs. See "Kubernetes integration tests" in
// README.md.
const (
	// k3sAPIPort is the port k3s serves its API on.
	k3sAPIPort = 6443
	// k3sAlias is the hostname the service is bound at, and so the hostname the
	// API server's certificate is issued for.
	k3sAlias = "k3s"
	// kubeconfigPath is where the cluster's admin kubeconfig is mounted in
	// every container that talks to it.
	kubeconfigPath = "/run/k3s/kubeconfig"
	// apiAuthFilePath is where the service mounts the API server's static token
	// file.
	apiAuthFilePath = "/etc/homelab/tokens.csv"
	// containerdPath is where k3s keeps the images it has pulled.
	containerdPath = "/var/lib/rancher/k3s/agent/containerd"
	// k3sStartTimeout bounds how long the service gets to start listening.
	k3sStartTimeout = 3 * time.Minute
	// k3sReadyTimeout bounds the wait for the API server and the node to become
	// ready once the service is listening.
	k3sReadyTimeout = "3m"
	// closeTimeout bounds Close. It runs on a context detached from the caller's,
	// so it needs a limit of its own.
	closeTimeout = time.Minute
)

// k3sEntrypoint nests cgroups before handing over to k3s, which only does that
// itself when it is PID 1 — which it is not under Dagger's init shim.
//
// The cgroup v2 block is from moby's hack/dind
// (https://github.com/moby/moby/blob/ed89041433a031cafc0a0f19cfe573c31688d377/hack/dind#L28-L37),
// used with permission of its author, as k3d and the daggerverse k3s module do.
// Moby is Apache-2.0: https://github.com/moby/moby/blob/ed89041433a031cafc0a0f19cfe573c31688d377/LICENSE
const k3sEntrypoint = `#!/bin/sh
set -o errexit
set -o nounset

if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  mkdir -p /sys/fs/cgroup/init
  xargs -rn1 < /sys/fs/cgroup/cgroup.procs > /sys/fs/cgroup/init/cgroup.procs || :
  sed -e 's/ / +/g' -e 's/^/+/' <"/sys/fs/cgroup/cgroup.controllers" >"/sys/fs/cgroup/cgroup.subtree_control"
fi

exec "$@"
`

// k3sCluster is one ephemeral k3s cluster.
type k3sCluster struct {
	// Name is the cluster name, unique per run.
	Name string
	// APIHost is the hostname a Dagger container reaches the cluster's
	// Kubernetes API server on. It is also a SAN on the API server's
	// certificate, so clients verify TLS rather than skipping it.
	APIHost string
	// Toolchain carries kubectl and curl, with nothing yet that ties it to this
	// cluster: execs derived from it are cached across runs.
	Toolchain *dagger.Container
	// Kubeconfig is the cluster's admin kubeconfig. It only ever exists inside
	// the engine.
	Kubeconfig *dagger.File
	// KubeSystemUID is the kube-system namespace's UID, read once when the
	// cluster came up. No two clusters share it, which is what makes it usable
	// as the cluster's identity.
	KubeSystemUID string

	// token is the admin credential: a static token the API server accepts as
	// system:masters.
	token *dagger.Secret
	// tokenAuth is token in the API server's token-auth-file format.
	tokenAuth *dagger.Secret
	// server is the k3s service, nil until Create has started it.
	server *dagger.Service
}

// newK3sCluster reserves a cluster name and its credential. It starts nothing —
// Create does — so that a caller can defer Close before anything exists to
// clean up.
func newK3sCluster(toolchain *dagger.Container) (*k3sCluster, error) {
	name, err := k3sClusterName()
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generating a k3s admin token: %w", err)
	}
	token := hex.EncodeToString(secret)

	return &k3sCluster{
		Name:      name,
		APIHost:   k3sAlias,
		Toolchain: toolchain,
		// Named per cluster, so no two clusters' secrets are ever the same one
		// to the engine.
		token:     dag.SetSecret("k3s-token-"+name, token),
		tokenAuth: dag.SetSecret("k3s-token-auth-"+name, tokenAuthLine(token)),
	}, nil
}

// k3sClusterName returns a collision-resistant cluster name.
func k3sClusterName() (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating a k3s cluster name: %w", err)
	}
	return "homelab-" + hex.EncodeToString(suffix), nil
}

// tokenAuthLine is one line of the API server's token-auth-file: the token, a
// user name and uid, and the groups it belongs to.
func tokenAuthLine(token string) string {
	return fmt.Sprintf("%s,homelab-admin,homelab-admin,%q\n", token, "system:masters")
}

// serverArgs is the `k3s server` invocation for this cluster.
func (c *k3sCluster) serverArgs() []string {
	return []string{
		"k3s", "server",
		// Nothing the integration tests use, and each is a workload to pull and
		// wait for.
		"--disable", "traefik",
		"--disable", "metrics-server",
		// The API server reaches nodes directly. The tunnel it would otherwise
		// use serves `kubectl logs` and `exec`, which nothing here needs.
		"--egress-selector-mode=disabled",
		// APIHost has to be a SAN on the API server's certificate, or every
		// client reaching the cluster by that name has to skip verification.
		"--tls-san", c.APIHost,
		// The credential the kubeconfig carries. The admin client certificate k3s
		// generates is unreachable from outside the service.
		"--kube-apiserver-arg", "token-auth-file=" + apiAuthFilePath,
	}
}

// service is the k3s server.
//
// Its containerd root is a PRIVATE cache volume. That keeps pulled images across
// runs, and it is also what lets containerd use overlayfs, which needs a real
// filesystem underneath rather than Dagger's own overlay. PRIVATE because two
// containerds cannot share a root. It is deliberately not wiped on start; the
// volume is keyed on the k3s image, so a version bump starts clean.
func (c *k3sCluster) service() *dagger.Service {
	return dag.Container().
		From(k3sImage).
		WithNewFile("/usr/local/bin/k3s-entrypoint.sh", k3sEntrypoint, dagger.ContainerWithNewFileOpts{
			Permissions: 0o755,
		}).
		WithEntrypoint([]string{"/usr/local/bin/k3s-entrypoint.sh"}).
		WithMountedSecret(apiAuthFilePath, c.tokenAuth).
		WithMountedCache(containerdPath,
			dag.CacheVolume("homelab-k3s-containerd-"+k3sImage),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModePrivate},
		).
		// Per-run state that has to be a real filesystem but must not outlive the
		// run: stale kubelet or CNI state would be read by the next cluster.
		WithMountedTemp("/var/lib/kubelet").
		WithMountedTemp("/var/lib/cni").
		WithMountedTemp("/var/log").
		WithExposedPort(k3sAPIPort).
		AsService(dagger.ContainerAsServiceOpts{
			Args: c.serverArgs(),
			// Through the entrypoint, which sets up cgroups before k3s starts.
			UseEntrypoint: true,
			// k3s runs containerd and a kubelet, which cannot create cgroups or
			// mount filesystems without this.
			InsecureRootCapabilities: true,
		})
}

// withService returns ctr able to reach the cluster's service.
//
// The cluster name is passed as an environment variable as well, so that every
// exec derived from this container is unique to this run. Dagger's exec cache
// keys on the command and the filesystem, neither of which captures that an exec
// against a service depends on live state. Everything that does not touch the
// cluster is built before this is applied, which is what keeps it cacheable.
func (c *k3sCluster) withService(ctr *dagger.Container) *dagger.Container {
	return ctr.
		WithServiceBinding(c.APIHost, c.server).
		WithEnvVariable("HOMELAB_K3S_CLUSTER", c.Name)
}

// WithCluster returns ctr able to talk to the cluster: the admin kubeconfig
// mounted at a fixed path with KUBECONFIG naming it. The kubeconfig is a file in
// the engine; no host file is read, written or merged, and no host context
// changes.
func (c *k3sCluster) WithCluster(ctr *dagger.Container) *dagger.Container {
	return c.withService(ctr).
		WithMountedFile(kubeconfigPath, c.Kubeconfig).
		WithEnvVariable("KUBECONFIG", kubeconfigPath)
}

// Server is the cluster's API server URL as its clients see it.
func (c *k3sCluster) Server() string {
	return fmt.Sprintf("https://%s:%d", c.APIHost, k3sAPIPort)
}

// Context is the kubeconfig context for the cluster.
func (c *k3sCluster) Context() string {
	return "k3s-" + c.Name
}

// Create starts the cluster, builds its kubeconfig and captures its identity.
func (c *k3sCluster) Create(ctx context.Context) error {
	// Bounded, because an engine that refuses privileged execs does not reject
	// the service — k3s simply never comes up, and the start blocks on the port
	// healthcheck until something gives up. Without this the workflow hangs
	// instead of naming its one prerequisite.
	startCtx, cancel := context.WithTimeout(ctx, k3sStartTimeout)
	defer cancel()

	server, err := c.service().Start(startCtx)
	if err != nil {
		return fmt.Errorf("the k3s server for cluster %s did not start within %s. "+
			"An engine configured with `insecureRootCapabilities: false` cannot run it, which "+
			"looks exactly like this; see \"Kubernetes integration tests\" in .dagger/README.md: %w",
			c.Name, k3sStartTimeout, err)
	}
	c.server = server

	withKubeconfig := c.withService(c.Toolchain).
		WithSecretVariable("K3S_TOKEN", c.token).
		WithExec([]string{"sh", "-c", `
set -eu
server=$1 context=$2 kubeconfig=$3 timeout=$4
ca=$(dirname "$kubeconfig")/ca.crt
mkdir -p "$(dirname "$kubeconfig")"

# /cacerts is the one endpoint k3s serves without credentials, and it is how the
# server's CA is obtained. The request skips verification because the CA is
# what it fetches; every call after this one verifies against it. The server
# only answers once its CA exists, so this also waits for k3s to be up.
curl --fail --silent --show-error --insecure \
	--retry 60 --retry-delay 2 --retry-connrefused --retry-all-errors \
	--output "$ca" "$server/cacerts"

kubectl config set-cluster "$context" --server "$server" \
	--certificate-authority "$ca" --embed-certs --kubeconfig "$kubeconfig"
kubectl config set-credentials "$context" --token "$K3S_TOKEN" --kubeconfig "$kubeconfig"
kubectl config set-context "$context" --cluster "$context" --user "$context" --kubeconfig "$kubeconfig"
kubectl config use-context "$context" --kubeconfig "$kubeconfig"
rm "$ca"

# Listening is not ready: the API server answers before it has finished
# starting, and nothing can be scheduled until the node has registered and is
# Ready. kubectl wait fails outright, rather than waiting, on a node that does
# not exist yet, so the node has to be waited for first.
deadline=$(( $(date +%s) + 180 ))
until [ -n "$(kubectl --kubeconfig "$kubeconfig" get nodes --output name 2>/dev/null)" ]; do
	if [ "$(date +%s)" -ge "$deadline" ]; then
		echo "no node registered with the API server within 180s" >&2
		exit 1
	fi
	sleep 1
done
kubectl --kubeconfig "$kubeconfig" wait --for condition=Ready node --all --timeout "$timeout"
`, "--", c.Server(), c.Context(), kubeconfigPath, k3sReadyTimeout})

	uid, err := withKubeconfig.
		WithEnvVariable("KUBECONFIG", kubeconfigPath).
		WithExec([]string{"kubectl", "get", "namespace", "kube-system", "--output", "jsonpath={.metadata.uid}"}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("k3s cluster %s came up but is not reachable at %s: %w",
			c.Name, c.Server(), withExecOutput(err))
	}

	c.Kubeconfig = withKubeconfig.File(kubeconfigPath)
	c.KubeSystemUID = strings.TrimSpace(uid)
	if c.KubeSystemUID == "" {
		return fmt.Errorf("k3s cluster %s reported an empty kube-system namespace UID", c.Name)
	}
	return nil
}

// Verify fails unless the kubeconfig still names the cluster this run created:
// the same API endpoint, the same context, and the same kube-system UID that
// was read when it came up.
//
// The first two catch a kubeconfig that was never swapped in; the UID is the one
// that cannot be faked by editing a kubeconfig, because a different cluster
// answering at the same address has a different one. This runs before any test
// does, so a misconfigured workflow fails instead of writing to someone's real
// cluster.
func (c *k3sCluster) Verify(ctx context.Context) error {
	out, err := c.WithCluster(c.Toolchain).
		WithExec([]string{"sh", "-c", `
set -eu
printf 'server=%s\n' "$(kubectl config view --minify --output 'jsonpath={.clusters[0].cluster.server}')"
printf 'context=%s\n' "$(kubectl config current-context)"
printf 'uid=%s\n' "$(kubectl get namespace kube-system --output 'jsonpath={.metadata.uid}')"
`}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("reading the identity of the cluster at %s: %w", c.Server(), withExecOutput(err))
	}
	return c.checkIdentity(out)
}

// checkIdentity compares what Verify's probe reported against what this cluster
// should report. The probe prints key=value lines rather than bare values, so an
// added field or a stray warning from kubectl cannot shift the parse.
func (c *k3sCluster) checkIdentity(probe string) error {
	got := map[string]string{}
	for _, line := range strings.Split(probe, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			got[key] = value
		}
	}

	var problems []string
	for _, check := range []struct {
		key, what, want string
	}{
		{"server", "API endpoint", c.Server()},
		{"context", "context", c.Context()},
		{"uid", "kube-system namespace UID", c.KubeSystemUID},
	} {
		if got[check.key] != check.want {
			problems = append(problems, fmt.Sprintf("%s is %q, want %q", check.what, got[check.key], check.want))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the kubeconfig does not point at the ephemeral cluster %s:\n  %s",
			c.Name, strings.Join(problems, "\n  "))
	}
	return nil
}

// WaitForWorkloads waits for every workload in namespace to finish rolling out,
// and returns the namespace's pod and event state when one doesn't.
//
// Helmfile returning successfully only means the manifests were accepted, so
// this is what decides whether a release is actually up. It asks kubectl about
// each of the three kinds that have a rollout rather than about Deployments
// alone, so a release whose workload is a StatefulSet or a DaemonSet needs
// nothing added here. A namespace with none of them fails, which is the right
// answer for a release whose tests are about to run.
func (c *k3sCluster) WaitForWorkloads(ctx context.Context, namespace, timeout string) error {
	_, err := c.WithCluster(c.Toolchain).
		WithExec([]string{"sh", "-c", `
set -eu
ns=$1
timeout=$2
workloads=$(kubectl --namespace "$ns" get deployment,statefulset,daemonset --output name)
if [ -z "$workloads" ]; then
	echo "namespace $ns has no deployment, statefulset or daemonset to wait for" >&2
	exit 1
fi
for workload in $workloads; do
	kubectl --namespace "$ns" rollout status "$workload" --timeout "$timeout"
done
`, "--", namespace, timeout}).
		Sync(ctx)
	if err == nil {
		return nil
	}
	return fmt.Errorf("the workloads in namespace %s did not roll out within %s: %w\n%s",
		namespace, timeout, withExecOutput(err), c.Diagnostics(ctx, namespace))
}

// Diagnostics returns what a namespace looks like when something in it did not
// come up. Best effort: it is only ever used to decorate an error, so a failure
// to collect it is reported in place of the diagnostics rather than replacing
// the error the caller already has.
func (c *k3sCluster) Diagnostics(ctx context.Context, namespace string) string {
	out, err := c.WithCluster(c.Toolchain).
		WithExec([]string{"sh", "-c", `
ns=$1
echo "--- pods in $ns"
kubectl --namespace "$ns" get pods --output wide || true
echo "--- pod detail in $ns"
kubectl --namespace "$ns" describe pods || true
echo "--- events in $ns"
kubectl --namespace "$ns" get events --sort-by=.lastTimestamp || true
`, "--", namespace}).
		Stdout(ctx)
	if err != nil {
		return fmt.Sprintf("--- could not collect diagnostics for namespace %s: %v\n", namespace, err)
	}
	return out
}

// Close stops the cluster, whatever happened to the run. Stopping the service
// is what frees everything the cluster occupies.
//
// Safe to call when Create never got that far. It runs on a context detached
// from the caller's: an interrupted run is exactly when the caller's context is
// already cancelled and the cluster still needs to go.
func (c *k3sCluster) Close(ctx context.Context) error {
	if c.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()

	var errs []error
	if _, err := c.server.Stop(ctx); err != nil {
		errs = append(errs, fmt.Errorf("stopping the k3s server for cluster %s: %w", c.Name, err))
	}
	return errors.Join(errs...)
}
