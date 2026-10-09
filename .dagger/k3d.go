package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"dagger/homelab/internal/dagger"
)

// Everything here is about one throwaway k3d cluster: creating it, reaching it
// from a Dagger container, proving a kubeconfig still points at it, and taking
// it down again. Nothing in this file knows what gets deployed to it.
//
// The cluster's nodes are containers in a Docker daemon, either a dind service
// inside the engine (the default, see dindService) or an external one named by
// DOCKER_HOST (see dockerHostAddress). The two differ only in which address the
// daemon's published ports answer on, which is what APIHost carries. See
// "Kubernetes integration tests" in README.md for the prerequisites each needs.
const (
	// k3dAPIPort is the port the cluster's API server is published on in the
	// Docker daemon's network namespace. Deliberately not 6443: that is the
	// port inside the node container, and keeping them distinct makes it
	// obvious which side of the publish an address refers to.
	k3dAPIPort = 6445
	// dindPort is the TCP port the dind service's daemon listens on.
	dindPort = 2375
	// dindAlias is the hostname the dind service is bound at, and so the
	// hostname the cluster's API server certificate is issued for.
	dindAlias = "dockerd"
	// kubeconfigPath is where the cluster's admin kubeconfig is mounted in
	// every container that talks to it.
	kubeconfigPath = "/run/k3d/kubeconfig"
	// k3dCreateTimeout bounds `k3d cluster create --wait`.
	k3dCreateTimeout = "5m"
	// dindStartTimeout bounds how long the dind service gets to start listening.
	dindStartTimeout = 2 * time.Minute
)

// k3dCluster is one ephemeral k3d cluster and the Docker daemon holding it.
type k3dCluster struct {
	// Name is the cluster name, unique per run. k3d names the kubeconfig's
	// cluster and context "k3d-<Name>".
	Name string
	// APIHost is the hostname a Dagger container reaches the cluster's
	// Kubernetes API server on. It is also a SAN on the API server's
	// certificate, so clients verify TLS rather than skipping it.
	APIHost string
	// Toolchain carries k3d and kubectl, DOCKER_HOST, and — for a dind daemon —
	// the service binding that makes APIHost resolve.
	Toolchain *dagger.Container
	// Kubeconfig is the cluster's admin kubeconfig with its server URL rewritten
	// to APIHost. It only ever exists inside the engine.
	Kubeconfig *dagger.File
	// KubeSystemUID is the kube-system namespace's UID, read once when the
	// cluster came up. No two clusters share it, which is what makes it usable
	// as the cluster's identity.
	KubeSystemUID string

	// dockerHost is the DOCKER_HOST every k3d call runs with.
	dockerHost string
	// docker is the dind service backing the cluster, nil for an external daemon.
	docker *dagger.Service
	// created records whether `k3d cluster create` succeeded, so that cleanup
	// after an early failure doesn't report a cluster it never made.
	created bool
}

// newK3dCluster reserves a cluster name and gets its Docker daemon running. It
// does not create the cluster — Create does — so that a caller can defer Close
// before anything exists to clean up.
func newK3dCluster(ctx context.Context, toolchain *dagger.Container, dockerHost string) (*k3dCluster, error) {
	name, err := k3dClusterName()
	if err != nil {
		return nil, err
	}
	c := &k3dCluster{Name: name}

	if dockerHost == "" {
		// Bounded, because an engine that refuses privileged execs does not
		// reject the service — dockerd simply never comes up, and the start
		// blocks on the port healthcheck until something gives up. Without this
		// the workflow hangs instead of naming its one prerequisite.
		startCtx, cancel := context.WithTimeout(ctx, dindStartTimeout)
		defer cancel()

		svc, err := dindService().Start(startCtx)
		if err != nil {
			return nil, fmt.Errorf("the docker daemon for k3d cluster %s did not come up within %s. "+
				"An engine configured with `insecureRootCapabilities: false` cannot run it, which "+
				"looks exactly like this; pass --docker-host to use a daemon outside the engine, or "+
				"see \"Kubernetes integration tests\" in .dagger/README.md: %w",
				name, dindStartTimeout, err)
		}
		c.docker = svc
		c.APIHost = dindAlias
		c.dockerHost = fmt.Sprintf("tcp://%s:%d", dindAlias, dindPort)
	} else {
		host, err := dockerHostAddress(dockerHost)
		if err != nil {
			return nil, err
		}
		c.APIHost = host
		c.dockerHost = dockerHost
	}

	c.Toolchain = c.withDocker(toolchain)
	return c, nil
}

// k3dClusterName returns a collision-resistant cluster name. k3d builds
// container and network names from it, so it stays short and DNS-safe.
func k3dClusterName() (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating a k3d cluster name: %w", err)
	}
	return "homelab-" + hex.EncodeToString(suffix), nil
}

// dockerHostAddress returns the host a Dagger container reaches an external
// Docker daemon's published ports on.
//
// The cluster's API port is published in the daemon's own network namespace, so
// the only endpoints that can work are ones whose host is routable from the
// engine. A unix socket names no host at all, and loopback names the Dagger
// container itself, so both are rejected here rather than left to fail later as
// a connection timeout with no explanation.
func dockerHostAddress(dockerHost string) (string, error) {
	u, err := url.Parse(dockerHost)
	if err != nil {
		return "", fmt.Errorf("parsing docker host %q: %w", dockerHost, err)
	}
	if u.Scheme != "tcp" {
		return "", fmt.Errorf("docker host %q cannot be used: a %q endpoint publishes the "+
			"cluster's API port where a Dagger container cannot reach it; pass tcp://host:port, "+
			"or leave the docker host empty to run the daemon inside the engine",
			dockerHost, u.Scheme)
	}
	host := u.Hostname()
	switch host {
	case "":
		return "", fmt.Errorf("docker host %q names no host", dockerHost)
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
		return "", fmt.Errorf("docker host %q names a loopback address, which inside a Dagger "+
			"container resolves to the container itself rather than to the daemon; use a name or "+
			"address the engine can route to", dockerHost)
	}
	return host, nil
}

// dindService is a Docker daemon to create k3d clusters in.
//
// Its data root is a PRIVATE cache volume, which is the only mount that works:
// dockerd's overlay2 driver needs a real filesystem underneath, and SHARED would
// deadlock two concurrent runs on dockerd's exclusive data-root lock.
func dindService() *dagger.Service {
	return dag.Container().
		From(dindImage).
		WithMountedCache("/var/lib/docker",
			// Keyed on the image tag, so a Renovate bump starts clean rather
			// than handing a new daemon an old daemon's data root.
			dag.CacheVolume("homelab-k3d-dind-"+dindImage),
			dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModePrivate},
		).
		// The image's entrypoint generates a CA and serves TLS on 2376 when this
		// is set, which would mean distributing client certs to every container
		// that talks to the daemon. The daemon is only reachable over the
		// engine's service network, so plain TCP is enough.
		WithEnvVariable("DOCKER_TLS_CERTDIR", "").
		WithExposedPort(dindPort).
		WithExposedPort(k3dAPIPort, dagger.ContainerWithExposedPortOpts{
			Description: "k3d Kubernetes API server",
			// k3d publishes this port when it creates the cluster, which is long
			// after the service has to report itself up.
			ExperimentalSkipHealthcheck: true,
		}).
		AsService(dagger.ContainerAsServiceOpts{
			// Through the image's entrypoint, which mounts cgroups and sets up
			// iptables before handing over to dockerd.
			UseEntrypoint: true,
			Args: []string{
				"dockerd",
				fmt.Sprintf("--host=tcp://0.0.0.0:%d", dindPort),
				"--tls=false",
			},
			// dockerd cannot create cgroups, mount overlayfs or write iptables
			// rules without this, and those are exactly what it needs to run the
			// k3s nodes.
			InsecureRootCapabilities: true,
		})
}

// withDocker returns ctr able to drive the cluster's Docker daemon.
//
// The cluster name is passed as an environment variable as well as in the
// commands that need it, so that every exec derived from this container is
// unique to this run. Dagger's exec cache keys on the command and the
// filesystem, neither of which captures that an exec against a service depends
// on live state.
func (c *k3dCluster) withDocker(ctr *dagger.Container) *dagger.Container {
	if c.docker != nil {
		ctr = ctr.WithServiceBinding(c.APIHost, c.docker)
	}
	return ctr.
		WithEnvVariable("DOCKER_HOST", c.dockerHost).
		WithEnvVariable("HOMELAB_K3D_CLUSTER", c.Name)
}

// WithCluster returns ctr able to talk to the cluster: the admin kubeconfig
// mounted at a fixed path with KUBECONFIG naming it, plus whatever withDocker
// adds. The kubeconfig is a file in the engine; no host file is read, written
// or merged, and no host context changes.
func (c *k3dCluster) WithCluster(ctr *dagger.Container) *dagger.Container {
	return c.withDocker(ctr).
		WithMountedFile(kubeconfigPath, c.Kubeconfig).
		WithEnvVariable("KUBECONFIG", kubeconfigPath)
}

// Server is the cluster's API server URL as its clients see it.
func (c *k3dCluster) Server() string {
	return fmt.Sprintf("https://%s:%d", c.APIHost, k3dAPIPort)
}

// Context is the kubeconfig context k3d writes for the cluster.
func (c *k3dCluster) Context() string {
	return "k3d-" + c.Name
}

// Create creates the cluster and captures its kubeconfig and identity.
func (c *k3dCluster) Create(ctx context.Context) error {
	// dockerd accepts connections before it has finished setting up storage and
	// networking, so the first call is retried rather than taken as a verdict.
	ready := []string{"sh", "-c", `
for _ in $(seq 1 30); do
	if k3d cluster list >/dev/null 2>&1; then exit 0; fi
	sleep 1
done
echo "the docker daemon at $DOCKER_HOST did not become usable within 30s" >&2
exit 1
`}

	prepared := c.Toolchain.WithExec(ready)
	if c.docker != nil {
		// The engine's own dind daemon keeps its data root in a reused cache
		// volume, so a run killed before its deferred cleanup can have left k3d
		// containers behind — with a restart policy that brings them back and a
		// published API port that would then collide. Only the dind daemon is
		// swept: it exists for this workflow alone, whereas a daemon the caller
		// supplied may be holding clusters that are none of its business.
		prepared = prepared.WithExec([]string{"k3d", "cluster", "delete", "--all"})
	}

	created := prepared.WithExec(c.createArgs())
	if _, err := created.Sync(ctx); err != nil {
		return fmt.Errorf("creating k3d cluster %s: %w", c.Name, withExecOutput(err))
	}
	c.created = true

	withKubeconfig := created.
		// Redirected into a file rather than read from stdout: `k3d kubeconfig
		// get` prints the cluster-admin client certificate and key, and a
		// function's stdout is its log.
		WithExec([]string{
			"sh", "-c",
			`mkdir -p "$(dirname "$2")" && exec k3d kubeconfig get "$1" > "$2"`,
			"--", c.Name, kubeconfigPath,
		}).
		// k3d derives the server URL from DOCKER_HOST, which is right for the
		// dind service and wrong whenever the daemon is reached by a different
		// name than its published ports are. Setting it from APIHost either way
		// means the kubeconfig cannot quietly point somewhere else.
		WithExec([]string{
			"kubectl", "config", "set-cluster", c.Context(),
			"--server", c.Server(),
			"--kubeconfig", kubeconfigPath,
		})

	uid, err := withKubeconfig.
		WithEnvVariable("KUBECONFIG", kubeconfigPath).
		WithExec([]string{"kubectl", "get", "namespace", "kube-system", "--output", "jsonpath={.metadata.uid}"}).
		Stdout(ctx)
	if err != nil {
		return fmt.Errorf("k3d cluster %s came up but is not reachable at %s: %w",
			c.Name, c.Server(), withExecOutput(err))
	}

	c.Kubeconfig = withKubeconfig.File(kubeconfigPath)
	c.KubeSystemUID = strings.TrimSpace(uid)
	if c.KubeSystemUID == "" {
		return fmt.Errorf("k3d cluster %s reported an empty kube-system namespace UID", c.Name)
	}
	return nil
}

// createArgs is the `k3d cluster create` invocation for this cluster.
func (c *k3dCluster) createArgs() []string {
	return []string{
		"k3d", "cluster", "create", c.Name,
		"--image", k3sImage,
		"--servers", "1",
		"--agents", "0",
		// Publish the API server on every interface of the Docker daemon's
		// network namespace; that is what makes it answer at APIHost.
		"--api-port", fmt.Sprintf("0.0.0.0:%d", k3dAPIPort),
		// APIHost has to be a SAN on the API server's certificate, or every
		// client reaching the cluster by that name has to skip verification.
		"--k3s-arg", "--tls-san=" + c.APIHost + "@server:*",
		// k3d defaults both of these to true: it would merge the cluster into
		// the caller's kubeconfig and switch its current context. The file it
		// would write is inside a Dagger exec and thrown away, but turning them
		// off makes the isolation a property of the command rather than of
		// where it happens to run.
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
		"--wait",
		"--timeout", k3dCreateTimeout,
	}
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
func (c *k3dCluster) Verify(ctx context.Context) error {
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
func (c *k3dCluster) checkIdentity(probe string) error {
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
func (c *k3dCluster) WaitForWorkloads(ctx context.Context, namespace, timeout string) error {
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
func (c *k3dCluster) Diagnostics(ctx context.Context, namespace string) string {
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

// Close destroys everything this cluster occupies, whatever happened to the run.
//
// Safe to call when Create never got that far: the cluster is only deleted when
// it was made, and only ever by name, so nothing else in the daemon is touched.
// For a dind daemon, stopping the service is what actually frees the resources —
// the delete is still run so that the external-daemon path is the same code.
func (c *k3dCluster) Close(ctx context.Context) error {
	var errs []error
	if c.created {
		if _, err := c.Toolchain.
			WithExec([]string{"k3d", "cluster", "delete", c.Name}).
			Sync(ctx); err != nil {
			errs = append(errs, fmt.Errorf("deleting k3d cluster %s: %w", c.Name, withExecOutput(err)))
		}
	}
	if c.docker != nil {
		if _, err := c.docker.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("stopping the docker daemon holding k3d cluster %s: %w", c.Name, err))
		}
	}
	return errors.Join(errs...)
}
