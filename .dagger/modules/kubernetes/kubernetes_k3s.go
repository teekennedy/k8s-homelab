package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"dagger/kubernetes/internal/dagger"
)

// k3sImage is what the clusters run. Keep its minor version in step with the
// production cluster's.
//
// renovate: datasource=docker depName=rancher/k3s
const k3sImage = "rancher/k3s:v1.37.1-k3s1@sha256:ca7f37d993d82ef0dcdcfecb2e0e2618ea541dbaffc620c8cedebe01a82acd0d"

// One k3s cluster: starting it, reaching it from a Dagger container, proving a
// kubeconfig still points at it, and stopping it. A cluster is either created
// for a single run and destroyed with it, or a dev cluster someone else is
// keeping up, which a run attaches to and leaves alone. See "Kubernetes
// integration tests" and "Dev clusters" in .dagger/docs/kubernetes.md.
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
	// k3sStatePath is where k3s keeps everything that makes a cluster that
	// cluster: its datastore and certificates, the images it has pulled, and
	// the volumes the local-path provisioner has created.
	k3sStatePath = "/var/lib/rancher/k3s"
	// containerdPath is where k3s keeps the images it has pulled.
	containerdPath = k3sStatePath + "/agent/containerd"
	// attachedKubeconfigPath is where the kubeconfig a caller supplied for an
	// existing cluster is mounted, before it is rewritten to kubeconfigPath.
	attachedKubeconfigPath = "/run/k3s/attached-kubeconfig"
	// k3sStartTimeout bounds how long the service gets to start listening.
	k3sStartTimeout = 3 * time.Minute
	// k3sReadyTimeout bounds the wait for the node to become Ready once the
	// service is listening.
	k3sReadyTimeout = "3m"
	// devClusterStarts is how many times a persistent cluster's k3s is started
	// before its exit is taken as final.
	devClusterStarts = "3"
	// closeTimeout bounds Close, which runs on a detached context.
	closeTimeout = time.Minute
	// k3sRunEnv is the variable that makes an exec unique to one run; see
	// Caching in .dagger/docs/kubernetes.md.
	k3sRunEnv = "HOMELAB_K3S_CLUSTER"
)

// k3sEntrypoint nests cgroups before handing over to k3s, which only does that
// itself when it is PID 1, and it is not under Dagger's init shim.
//
// With K3S_STARTS above 1 it starts k3s again when it exits, the way a service
// manager would. A resumed cluster needs that: its container has a new address,
// and kube-proxy exits when it sees a node's addresses change, taking the rest
// of k3s with it. The start after that finds the new address already recorded.
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

starts=${K3S_STARTS:-1}
if [ "$starts" -le 1 ]; then
  exec "$@"
fi
while :; do
  status=0
  "$@" || status=$?
  starts=$((starts - 1))
  if [ "$starts" -le 0 ]; then
    exit "$status"
  fi
  echo "k3s exited with status $status; starting it again" >&2
done
`

// devClusterName is what a dev cluster may be called. The name ends up in a
// cache volume's key and in a kubeconfig context.
var devClusterName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// checkDevClusterName fails unless name is one a dev cluster may have.
func checkDevClusterName(name string) error {
	if !devClusterName.MatchString(name) {
		return fmt.Errorf("dev cluster name %q must match %s", name, devClusterName)
	}
	return nil
}

// devClusterState is the cache volume the dev cluster called name keeps its
// state in.
func devClusterState(name string) *dagger.CacheVolume {
	return dag.CacheVolume("homelab-k3s-dev-" + name)
}

// k3sCluster is one k3s cluster.
type k3sCluster struct {
	// Name is the cluster name: random for a cluster created for one run, the
	// kubeconfig's context for one that was attached to.
	Name string
	// Port is the port clients reach the API server on at APIHost.
	Port int
	// Persistent keeps the cluster's state in a cache volume named after it, so
	// that starting a cluster of the same name again resumes it.
	Persistent bool
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
	// attached is the kubeconfig of a cluster this run did not start, nil for
	// one it did. Its server is the service's address on whoever supplied it.
	attached *dagger.Secret
	// run distinguishes this run's execs from any other's against the same
	// cluster, so that none of them is served from cache.
	run string
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
		Port:      k3sAPIPort,
		APIHost:   k3sAlias,
		Toolchain: toolchain,
		// Named per cluster, so the engine never treats two as the same secret.
		token:     dag.SetSecret("k3s-token-"+name, token),
		tokenAuth: dag.SetSecret("k3s-token-auth-"+name, tokenAuthLine(token)),
		run:       name,
	}, nil
}

// newDevCluster describes a cluster that keeps its state under name, with an
// admin token chosen by the caller so that a kubeconfig written for it stays
// valid when it is started again. It starts nothing.
func newDevCluster(ctx context.Context, name string, token *dagger.Secret) (*k3sCluster, error) {
	if err := checkDevClusterName(name); err != nil {
		return nil, err
	}
	plaintext, err := token.Plaintext(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the admin token for dev cluster %s: %w", name, err)
	}
	plaintext = strings.TrimSpace(plaintext)
	// The token file is CSV, and a short token is a guessable admin credential.
	if len(plaintext) < 32 || strings.ContainsAny(plaintext, ", \t\n\"") {
		return nil, fmt.Errorf("the admin token for dev cluster %s must be at least 32 characters, "+
			"with no commas, quotes or whitespace", name)
	}
	return &k3sCluster{
		Name:       name,
		APIHost:    k3sAlias,
		Persistent: true,
		tokenAuth:  dag.SetSecret("k3s-token-auth-"+name, tokenAuthLine(plaintext)),
	}, nil
}

// attachK3sCluster describes a cluster that is already running: server is its
// API server, and kubeconfig an admin kubeconfig for it. Create then connects
// to it instead of starting one, and Close leaves it running.
func attachK3sCluster(toolchain *dagger.Container, server *dagger.Service, kubeconfig *dagger.Secret) (*k3sCluster, error) {
	run, err := k3sClusterName()
	if err != nil {
		return nil, err
	}
	return &k3sCluster{
		APIHost:   k3sAlias,
		Toolchain: toolchain,
		server:    server,
		attached:  kubeconfig,
		run:       run,
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
	args := []string{
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
	if c.Persistent {
		// The default is the container's hostname, which is new on every start: a
		// resumed cluster would find its pods bound to a node that is gone.
		args = append(args, "--node-name", c.Name)
	}
	return args
}

// service is the k3s server. A cluster created for one run keeps only its
// containerd root, in a PRIVATE cache volume keyed on the k3s image; see
// Limitations in .dagger/docs/kubernetes.md. A persistent one keeps all of its state, in a
// volume of its own that only one server may hold at a time.
func (c *k3sCluster) service() *dagger.Service {
	ctr := dag.Container().
		From(k3sImage).
		WithNewFile("/usr/local/bin/k3s-entrypoint.sh", k3sEntrypoint, dagger.ContainerWithNewFileOpts{
			Permissions: 0o755,
		}).
		WithEntrypoint([]string{"/usr/local/bin/k3s-entrypoint.sh"}).
		WithMountedSecret(apiAuthFilePath, c.tokenAuth)
	if c.Persistent {
		ctr = ctr.
			WithEnvVariable("K3S_STARTS", devClusterStarts).
			WithMountedCache(k3sStatePath, devClusterState(c.Name),
				dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked},
			)
	} else {
		ctr = ctr.WithMountedCache(containerdPath,
			dag.CacheVolume("homelab-k3s-containerd-"+k3sImage),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModePrivate},
		)
	}
	return ctr.
		// Real filesystems that must not outlive the run.
		WithMountedTemp("/var/lib/kubelet").
		WithMountedTemp("/var/lib/cni").
		WithMountedTemp("/var/log").
		WithExposedPort(k3sAPIPort).
		AsService(dagger.ContainerAsServiceOpts{
			Args:                     c.serverArgs(),
			UseEntrypoint:            true,
			InsecureRootCapabilities: true,
		})
}

// withService returns ctr bound to the cluster's service. The run goes in the
// environment so that every exec derived from the result is unique to this
// run; see Caching in .dagger/docs/kubernetes.md.
func (c *k3sCluster) withService(ctr *dagger.Container) *dagger.Container {
	return ctr.
		WithServiceBinding(c.APIHost, c.server).
		WithEnvVariable(k3sRunEnv, c.run)
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
	return fmt.Sprintf("https://%s:%d", c.APIHost, c.Port)
}

// Context is the kubeconfig context for the cluster.
func (c *k3sCluster) Context() string {
	if c.attached != nil {
		return c.Name
	}
	return "k3s-" + c.Name
}

// attach connects to a cluster that is already running. Its kubeconfig names
// the API server as its owner reaches it, so the copy used here is pointed at
// the service's alias instead, on the same port.
func (c *k3sCluster) attach(ctx context.Context) error {
	withKubeconfig := c.withService(c.Toolchain).
		WithMountedSecret(attachedKubeconfigPath, c.attached).
		WithExec([]string{"sh", "-c", `
set -eu
attached=$1 kubeconfig=$2 host=$3
mkdir -p "$(dirname "$kubeconfig")"
kubectl --kubeconfig "$attached" config view --minify --flatten > "$kubeconfig"
chmod 600 "$kubeconfig"

cluster=$(kubectl --kubeconfig "$kubeconfig" config view --output 'jsonpath={.clusters[0].name}')
server=$(kubectl --kubeconfig "$kubeconfig" config view --output 'jsonpath={.clusters[0].cluster.server}')
port=${server##*:}
port=${port%%/*}
case $port in
	''|*[!0-9]*) echo "cannot read a port from the kubeconfig's server, $server" >&2; exit 1 ;;
esac
kubectl --kubeconfig "$kubeconfig" config set-cluster "$cluster" --server "https://$host:$port" >/dev/null

printf 'context=%s\n' "$(kubectl --kubeconfig "$kubeconfig" config current-context)"
printf 'port=%s\n' "$port"
printf 'uid=%s\n' "$(kubectl --kubeconfig "$kubeconfig" get namespace kube-system --output 'jsonpath={.metadata.uid}')"
`, "--", attachedKubeconfigPath, kubeconfigPath, c.APIHost})

	out, err := withKubeconfig.Stdout(ctx)
	if err != nil {
		return fmt.Errorf("attaching to the cluster the supplied kubeconfig names: %w", withExecOutput(err))
	}
	identity := parseProbe(out)
	if _, err := fmt.Sscanf(identity["port"], "%d", &c.Port); err != nil {
		return fmt.Errorf("reading the API server port of the attached cluster: %w", err)
	}
	c.Name = identity["context"]
	c.KubeSystemUID = identity["uid"]
	if c.Name == "" || c.KubeSystemUID == "" {
		return fmt.Errorf("the attached cluster reported no context or kube-system namespace UID:\n%s", out)
	}
	c.Kubeconfig = withKubeconfig.File(kubeconfigPath)
	return nil
}

// Create starts the cluster, builds its kubeconfig and captures its identity.
// For a cluster that was attached to, it connects to it instead.
func (c *k3sCluster) Create(ctx context.Context) error {
	if c.attached != nil {
		return c.attach(ctx)
	}
	// Bounded because an engine that refuses privileged execs does not reject
	// the service: k3s never comes up and the start blocks on its port.
	startCtx, cancel := context.WithTimeout(ctx, k3sStartTimeout)
	defer cancel()

	server, err := c.service().Start(startCtx)
	if err != nil {
		return fmt.Errorf("the k3s server for cluster %s did not start within %s. "+
			"An engine configured with `insecureRootCapabilities: false` cannot run it, which "+
			"looks exactly like this; see \"Kubernetes integration tests\" in .dagger/docs/kubernetes.md: %w",
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

// parseProbe reads the key=value lines a probe exec prints.
func parseProbe(out string) map[string]string {
	probe := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			probe[key] = value
		}
	}
	return probe
}

// checkIdentity compares Verify's key=value probe output with this cluster's
// identity.
func (c *k3sCluster) checkIdentity(probe string) error {
	got := parseProbe(probe)

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
		return fmt.Errorf("the kubeconfig does not point at cluster %s:\n  %s",
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

// Close stops the cluster, unless it was attached to: that one is someone
// else's to stop. Safe to call when Create never got that far. It runs on a
// context detached from the caller's, which is already cancelled when a run is
// interrupted.
func (c *k3sCluster) Close(ctx context.Context) error {
	if c.server == nil || c.attached != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()

	if _, err := c.server.Stop(ctx); err != nil {
		return fmt.Errorf("stopping the k3s server for cluster %s: %w", c.Name, err)
	}
	return nil
}

// DevCluster is a k3s cluster that keeps its state between runs: starting a
// cluster of the same name again resumes it, with its releases and volumes.
// It runs for as long as the call that started it does, so it is used with
// `up`, which also publishes the API server on the host:
//
//	dagger api call kubernetes dev-cluster --name=dev --token=file:./token up --ports=6443:6443
//
// scripts/dev-cluster.sh wraps this, and writes the kubeconfig. See "Dev
// clusters" in .dagger/docs/kubernetes.md.
func (m *Kubernetes) DevCluster(ctx context.Context,
	// Names the cluster's state, and so which cluster this is.
	name string,
	// Admin token the API server accepts. Whoever holds it is cluster-admin.
	token *dagger.Secret,
) (*dagger.Service, error) {
	cluster, err := newDevCluster(ctx, name, token)
	if err != nil {
		return nil, err
	}
	return cluster.service(), nil
}

// DevClusterDelete deletes a dev cluster's state. The cluster must be stopped:
// a running one holds its state volume, and this waits for it.
//
//	dagger api call kubernetes dev-cluster-delete --name=dev
func (m *Kubernetes) DevClusterDelete(ctx context.Context,
	name string,
) (string, error) {
	if err := checkDevClusterName(name); err != nil {
		return "", err
	}
	// A cache volume cannot be deleted, only emptied. The run is in the
	// environment because an exec that only touches a cache volume is otherwise
	// served from cache the second time.
	run, err := k3sClusterName()
	if err != nil {
		return "", err
	}
	_, err = dag.Container().
		From(k3sImage).
		WithMountedCache("/state", devClusterState(name),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked},
		).
		WithEnvVariable(k3sRunEnv, run).
		WithExec([]string{"find", "/state", "-mindepth", "1", "-delete"}).
		Sync(ctx)
	if err != nil {
		return "", fmt.Errorf("deleting the state of dev cluster %s: %w", name, withExecOutput(err))
	}
	return fmt.Sprintf("Deleted the state of dev cluster %s", name), nil
}
