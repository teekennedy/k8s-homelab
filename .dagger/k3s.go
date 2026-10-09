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

// One throwaway k3s cluster: starting it, reaching it from a Dagger container,
// proving a kubeconfig still points at it, and stopping it. See "Kubernetes
// integration tests" in README.md.
const (
	// k3sAPIPort is the port k3s serves its API on.
	k3sAPIPort = 6443
	// k3sAlias is the hostname the service is bound at, and the one the API
	// server's certificate is issued for.
	k3sAlias = "k3s"
	// kubeconfigPath is where the admin kubeconfig is mounted.
	kubeconfigPath = "/run/k3s/kubeconfig"
	// apiAuthFilePath is where the API server's static token file is mounted.
	apiAuthFilePath = "/etc/homelab/tokens.csv"
	// containerdPath is where k3s keeps the images it has pulled.
	containerdPath = "/var/lib/rancher/k3s/agent/containerd"
	// k3sStartTimeout bounds how long the service gets to start listening.
	k3sStartTimeout = 3 * time.Minute
	// k3sReadyTimeout bounds the wait for the node to become Ready once the
	// service is listening.
	k3sReadyTimeout = "3m"
	// closeTimeout bounds Close, which runs on a detached context.
	closeTimeout = time.Minute
)

// k3sEntrypoint nests cgroups before handing over to k3s, which only does that
// itself when it is PID 1, and it is not under Dagger's init shim.
//
// The cgroup v2 block is from moby's hack/dind
// (https://github.com/moby/moby/blob/ed89041433a031cafc0a0f19cfe573c31688d377/hack/dind#L28-L37),
// Apache-2.0: https://github.com/moby/moby/blob/ed89041433a031cafc0a0f19cfe573c31688d377/LICENSE
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
	// APIHost is the hostname containers reach the API server on, and a SAN on
	// its certificate.
	APIHost string
	// Toolchain carries kubectl and curl and nothing tied to this cluster, so
	// execs derived from it are cached across runs.
	Toolchain *dagger.Container
	// Kubeconfig is the admin kubeconfig. It only ever exists inside the engine.
	Kubeconfig *dagger.File
	// KubeSystemUID is the kube-system namespace's UID, read once at creation.
	// It is the cluster's identity: no two clusters share it.
	KubeSystemUID string

	// token is the admin credential, accepted as system:masters.
	token *dagger.Secret
	// tokenAuth is token in the API server's token-auth-file format.
	tokenAuth *dagger.Secret
	// server is the k3s service, nil until Create has started it.
	server *dagger.Service
}

// newK3sCluster reserves a cluster name and credential. It starts nothing, so a
// caller can defer Close before Create.
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
		// Named per cluster, so the engine never treats two as the same secret.
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

// tokenAuthLine is one line of the API server's token-auth-file: token, user
// name, uid and groups.
func tokenAuthLine(token string) string {
	return fmt.Sprintf("%s,homelab-admin,homelab-admin,%q\n", token, "system:masters")
}

// serverArgs is the `k3s server` invocation for this cluster.
func (c *k3sCluster) serverArgs() []string {
	return []string{
		"k3s", "server",
		// Workloads to pull and wait for that nothing here uses.
		"--disable", "traefik",
		"--disable", "metrics-server",
		// Skips the tunnel that serves `kubectl logs` and `exec`.
		"--egress-selector-mode=disabled",
		// Without the SAN, clients reaching APIHost must skip verification.
		"--tls-san", c.APIHost,
		// The credential the kubeconfig carries; k3s's own admin certificate is
		// unreachable from outside the service.
		"--kube-apiserver-arg", "token-auth-file=" + apiAuthFilePath,
	}
}

// service is the k3s server. Its containerd root is a PRIVATE cache volume keyed
// on the k3s image; see Limitations in README.md.
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
		// Real filesystems that must not outlive the run.
		WithMountedTemp("/var/lib/kubelet").
		WithMountedTemp("/var/lib/cni").
		WithMountedTemp("/var/log").
		WithExposedPort(k3sAPIPort).
		AsService(dagger.ContainerAsServiceOpts{
			Args: c.serverArgs(),
			UseEntrypoint: true,
			InsecureRootCapabilities: true,
		})
}

// withService returns ctr bound to the cluster's service. The cluster name goes
// in the environment so that every exec derived from the result is unique to
// this run; see Caching in README.md.
func (c *k3sCluster) withService(ctr *dagger.Container) *dagger.Container {
	return ctr.
		WithServiceBinding(c.APIHost, c.server).
		WithEnvVariable("HOMELAB_K3S_CLUSTER", c.Name)
}

// WithCluster returns ctr able to talk to the cluster: the admin kubeconfig
// mounted with KUBECONFIG naming it.
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
	// Bounded because an engine that refuses privileged execs does not reject
	// the service: k3s never comes up and the start blocks on its port.
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

# /cacerts is served without credentials. Verification is skipped because this
# fetches the CA; every later call verifies. It answers only once k3s is up.
curl --fail --silent --show-error --insecure \
	--retry 60 --retry-delay 2 --retry-connrefused --retry-all-errors \
	--output "$ca" "$server/cacerts"

kubectl config set-cluster "$context" --server "$server" \
	--certificate-authority "$ca" --embed-certs --kubeconfig "$kubeconfig"
kubectl config set-credentials "$context" --token "$K3S_TOKEN" --kubeconfig "$kubeconfig"
kubectl config set-context "$context" --cluster "$context" --user "$context" --kubeconfig "$kubeconfig"
kubectl config use-context "$context" --kubeconfig "$kubeconfig"
rm "$ca"

# kubectl wait fails outright on a node that has not registered yet, so wait
# for the node to exist first.
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
// the same endpoint, context and kube-system UID. The UID cannot be faked by
// editing a kubeconfig. It runs before any test does, so a misconfigured
// workflow fails instead of writing to another cluster.
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

// checkIdentity compares Verify's key=value probe output with this cluster's
// identity.
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

// WaitForWorkloads waits for every Deployment, StatefulSet and DaemonSet in
// namespace to roll out, and attaches the namespace's diagnostics to the error
// when one doesn't. A namespace with none of them fails.
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

// Diagnostics returns a namespace's pods and events. Best effort: a failure to
// collect them is reported in their place, not instead of the caller's error.
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

// Close stops the cluster. Safe to call when Create never got that far. It runs
// on a context detached from the caller's, which is already cancelled when a run
// is interrupted.
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
