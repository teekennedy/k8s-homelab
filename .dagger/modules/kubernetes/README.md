# kubernetes

k3s clusters run as Dagger services, and the integration workflow that deploys
releases onto one with the repo's own helmfile and runs their pytest suites
against it.

| Function | What it does |
| --- | --- |
| `integration-test` | Deploy the dev environment's releases to a throwaway cluster and test them |
| `integration-reports` | The same run, returning the JUnit XML reports |
| `integration-run` | The workflow itself; called through the module's own API by the two above |
| `dev-cluster` | A named cluster that keeps its state between runs |
| `dev-cluster-delete` | Delete a stopped dev cluster's state |

None of them is a check; see below for why. All take the toolchain through the
constructor's `--container`, which defaults to the devenv `ci` and
`integration` profiles: the ci toolchain plus kubectl and curl.

## Integration tests

Some things cannot be checked by rendering a chart: whether reflector actually
copies a ConfigMap, for instance. `integration-test` starts a
throwaway single-node k3s cluster, deploys the releases under test onto it with
this repo's own helmfile, waits for them to become healthy, and runs each
release's pytest suite against the result — then destroys the cluster.

```bash
# Deploy and test everything the dev environment enables
dagger call kubernetes integration-test

# Narrow that to specific releases
dagger call kubernetes integration-test --releases=reflector

# Same run, but keep the JUnit XML reports
dagger call kubernetes integration-reports export --path=./reports

# Sync twice before testing, to check that re-syncing an existing cluster works
dagger call kubernetes integration-test --repeat-sync
```

The workflow is repeatable with no manual steps: every run gets a fresh cluster
under a new random name and destroys it again, so running it twice in a row
needs nothing in between.

It is **not** a `+check`, on purpose, and nothing in `dagger check` runs it. It
needs an engine that allows privileged execs, and `dagger check` runs whatever a
PR contains: a PR that changes `.woodpecker/ci.yaml`, these modules or the pytest
suites it runs could use a privileged exec to escape the container. It runs
only when someone invokes it, on an engine that someone has chosen to give
`insecureRootCapabilities` — and the intent is to run it on trusted refs only,
after a merge and before a deploy, rather than on PRs. The Python check keeps
passing on a machine with no cluster at all.

### Prerequisites

The cluster is a `k3s server` run as a Dagger service, and `k3s` cannot run
containers without privileges: it starts containerd and a kubelet, which
create cgroups and mount filesystems. The engine therefore has to allow
privileged execs:

```json
{ "security": { "insecureRootCapabilities": true } }
```

An engine that does not allow them does not reject the service: `k3s` just never
comes up, so the service start is bounded at 3 minutes and the workflow then
fails naming this prerequisite rather than hanging. A local Docker-based engine
is the usual answer:

```bash
unset _EXPERIMENTAL_DAGGER_RUNNER_HOST   # don't use the in-cluster engine
dagger call kubernetes integration-test
```

There is no Docker daemon, k3d or `DOCKER_HOST` involved: nothing outside the
engine is needed.

### Networking and credentials

```
Dagger exec (helmfile, kubectl, pytest)
  │  KUBECONFIG=/run/k3s/kubeconfig → https://k3s:6443
  ▼
k3s service, bound at the alias "k3s"
  └─ 6443  the API server
```

A kubeconfig pointing at `localhost` is useless inside a Dagger container, so
the API server's certificate is issued for the service's alias
(`--tls-san k3s`) and the kubeconfig names that alias.

The kubeconfig is built by the workflow, not taken from k3s. The API server is
started with `--kube-apiserver-arg token-auth-file=...`, naming a file that
holds a random per-run token mapped to `system:masters`; the file is a Dagger
secret mounted into the service. The client side gets its CA from `/cacerts`,
the one endpoint k3s serves without credentials, and `kubectl config` assembles
a kubeconfig from that CA and the token. The fetch of `/cacerts` is the only
request that does not verify the server — it is what supplies the CA — and it
stays inside the engine's service network. Every call after it verifies.

### Kubeconfig isolation

The admin kubeconfig only ever exists as a `*dagger.File` inside the engine,
mounted at `/run/k3s/kubeconfig` in the containers that need it. Concretely:

- No host file is read, written or merged, and no host context changes.
- The token is a Dagger secret, so it is masked in logs, and it is random per
  run, so a leaked one opens nothing once the service has stopped.
- Cluster names are random (`homelab-<12 hex>`), and the only thing the run
  ever stops is its own service.
- `Close` is deferred before the cluster exists, so a failed deploy, a failing
  test or a panic all still take the cluster down. It runs on a context
  detached from the caller's, with a one-minute limit of its own, because an
  interrupted run is exactly when the caller's context is already cancelled.

### Deploying with the existing helmfile

There is no separate state file for development clusters. The workflow runs
`helmfile sync` against `helmfile.yaml.gotmpl` with `--environment dev`,
whose values come from `config/dev.cue` by way of
`config/gen/dev/env.json` like every other environment.

Release enablement is the `apps` map in those values, read by the helmfile
templates' `installedTemplate`. A `--selector` can narrow what a sync touches
but cannot turn a release on, which makes the environment the only usable source
of truth — so that is where the workflow gets its release set from, and
`--releases` only narrows it. Asking for a release the environment disables is an
error naming the file to edit, rather than a sync that silently installs nothing.

`dev` therefore disables everything by default (`_appsDisabled` in
`config/base.cue`, derived from the full production list) and enables only what
has tests.

Helmfile is given the cluster's kubeconfig through `KUBECONFIG` *and* the
cluster's own context through `--kube-context`. Between them there is nothing
left for an ambient current-context to decide: a kubeconfig that is not this
cluster's has no context by that name, and helmfile fails rather than deploying
somewhere else.

### Health, before tests

The cluster is not used until the API server answers `/readyz` and the node is
`Ready`. `helmfile sync` returning successfully then only means the manifests
were accepted, so the workflow waits on Kubernetes itself — `kubectl rollout
status` over every Deployment, StatefulSet and DaemonSet in the release's
namespace, with a 5m timeout. Namespace-scoped and workload-agnostic, so a
release whose workload isn't a Deployment needs nothing added; a namespace with
none of the three fails, which is the right answer for a release whose tests are
about to run. When the wait fails, the namespace's pod status, pod detail and
events are attached to the error.

The waits for several releases run concurrently, so their timeouts don't sum.

Only then does it prove the kubeconfig still names the cluster this run created:
same API endpoint, same context, and the same `kube-system` namespace UID that
was read when the cluster came up. The UID is the part a kubeconfig edit cannot
fake — a different cluster answering at the same address has a different one.

### How the markers keep production safe

A developer's shell very often has `KUBECONFIG` pointing at the real cluster,
and these tests create and delete namespaces. So they never run because a
kubeconfig happens to be valid; they run because someone selected them.

- Every test carries `@pytest.mark.kubernetes`, registered in the project's
  `pyproject.toml`.
- That same `pyproject.toml` sets `addopts = "-m 'not kubernetes'"`. This is the
  mechanism — it covers a bare `pytest` in an editor or a shell as well as
  `dagger check python:test`, and it is the only place that *can* cover the
  former.
- `python:test` deliberately passes **no** `-m`. A command-line `-m`
  replaces `addopts` rather than narrowing it, so one passed from the runner
  would re-enable the tests a project deselected for itself and deselect the ones
  a project runs on purpose — `jellyfin-exporter` registers `integration`
  precisely so those tests run by default against its stub server.
- Deselecting every test in a project makes pytest exit 5. `Test` treats that as
  a pass, which is how a tests-only project with nothing left to run doesn't
  fail the Python check.
- The integration workflow selects `-m kubernetes` explicitly, and a run that
  collected nothing is reported as a failure rather than a pass.

The trade-off is that a new test project which forgets the `addopts` line is not
caught statically. It fails loudly instead: the ci toolchain has no Kubernetes
client and no `KUBECONFIG`, so the suite errors out in `dagger check` rather than
reaching any cluster.

Within the suite, missing credentials, an unreachable API server or a rejected
credential raise rather than skip: once the tests have been selected, not
running is a failure.

### Adding integration tests to another release

1. Create `k8s/<tier>/<release>/tests/` as a Python project — copy the shape of
   `k8s/foundation/reflector/tests/`: a `pyproject.toml` registering the
   `kubernetes` marker and deselecting it by default, a `uv.lock`, a
   `conftest.py` that loads the shared fixtures from `k8s/testlib/` and
   defines the suite's namespace fixture, and the tests themselves.
2. Mark every test `@pytest.mark.kubernetes`.
3. Add `tests/` to the chart's `.helmignore`, so test files stay out of the
   packaged chart.
4. Enable the release in `config/dev.cue`, then
   `dagger generate cue:export -y`.
5. `dagger call kubernetes integration-test --releases=<release>`.

Nothing in `.dagger` needs changing, and step 4 is what adds the release to the
default run: with no `--releases`, the workflow deploys and tests everything the
dev environment enables. The workflow finds the test directory from the
release's chart path, deploys the release, waits on its namespace and runs
pytest; what to assert is entirely the test project's business.

### Caching

The call is cached at four levels, and the workflow is arranged so each one can
do its job.

1. **The outer function call.** Dagger caches a function's result on its
   arguments. `source` is a content-addressed directory filtered by the
   `+ignore` list, so re-running with nothing changed in
   `helmfile.yaml.gotmpl`, the dev environment's values or anything under
   `k8s/` returns the earlier verdict without starting a cluster. Editing
   anything outside that list (`README.md`, `.woodpecker/`, `nix/`) is also a
   hit. The filter is coarse — it cannot know which releases a run will deploy —
   so a change to *any* chart under `k8s/` re-runs this level, which costs only
   the plan.
2. **The inner function call.** `integration-test` and
   `integration-reports` plan against the whole source, then pass
   `integration-run` only what the run reads: the state file, the
   dev values, the shared charts, the shared test library (`k8s/testlib/`)
   and the chosen releases' charts (tests included). It is called through the module's own API, which needs the
   experimental self-calls capability (`SELF_CALLS`, kept in this module's
   `dagger.json`). The workflow itself therefore re-runs only when something
   it deploys or tests changes.
3. **Execs that do not touch the cluster.** The toolchain, the helmfile planning
   (`helmfile build` and `list`) and each test project's `uv sync` are all built
   *before* anything cluster-specific is applied to the container, so they are
   cached whatever the cluster looks like. The `uv sync` copies in only
   `pyproject.toml` and `uv.lock` before installing, and runs on the toolchain
   *without* the repo mounted — a mount is part of an exec's cache key — so
   editing a test reinstalls nothing. The shared test library is imported by
   path rather than installed, so editing it reinstalls nothing either.
4. **Execs that do.** Everything from the first use of the service on is unique
   to the run, by design: `HOMELAB_K3S_CLUSTER=<random name>` is in its
   environment, and the kubeconfig it mounts carries a random token. Caching one
   would mean serving "helmfile sync succeeded" for a cluster that has since been
   destroyed.

`integration-run` returns plain data — strings, with the JUnit reports
as XML text — and never a `Directory`. A directory built from execs that used a
service and a secret cannot outlive the session that made it, and a function
result holding one is silently not cached across sessions: the inner call
re-ran every time until it returned text instead.

What persists *between* runs that are not cached is the containerd root, in a
`PRIVATE` cache volume (about 540MB with reflector's images in it). The k3s
image itself is an ordinary image layer, cached by the engine.

Measured on engine v0.21.10, one release (reflector), same host:

| Run | k3d in dind | k3s service |
| --- | --- | --- |
| Nothing changed | 2.8s | 3s |
| File outside the `+ignore` list changed | — | 4s |
| An unrelated chart changed | 107s | 3s |
| A reflector test changed | 99s | 55s |
| First run, warm toolchain, empty volumes | — | 91s |

A run that is not cached spends roughly: 8s for k3s to boot and the node to go
Ready, 19s in `helmfile sync`, 11s in `rollout status`, 5s in pytest and the rest
in loading the module and planning.

### Limitations

- Needs an engine that allows privileged execs, which the in-cluster engine
  does not. See Prerequisites.
- k3s's containerd root is a `PRIVATE` cache volume, keyed on the k3s image tag.
  It has to be a real filesystem for overlayfs, and two containerds cannot share
  a root. It is deliberately not wiped between runs, so it is what makes images
  pulled once stay pulled; a run killed mid-flight leaves its containers' metadata
  in it for the next k3s to find.
- `k3sImage` is pinned to the production cluster's Kubernetes minor version, so
  the tests run against the version it does.

## Dev clusters

The integration workflow creates a cluster and destroys it in the same call. A
dev cluster is the same k3s server kept running under a name, for deploying to
and poking at by hand before anything is committed:

```bash
scripts/dev-cluster.sh up dev1             # start it, or resume it
eval "$(scripts/dev-cluster.sh env dev1)"  # point this shell at it
kubectl get nodes
helmfile --environment dev --kube-context dev1 --selector name=reflector sync

scripts/dev-cluster.sh test dev1 --releases=reflector   # deploy and test on it
scripts/dev-cluster.sh down dev1           # stop it, keeping its state
scripts/dev-cluster.sh destroy dev1        # stop it and delete its state
```

Several can run side by side under different names.

### Lifetime

A Dagger service lives exactly as long as the session that started it, so a
cluster cannot outlive its `dagger call`. `up` therefore leaves one running in
the background — `dagger call kubernetes dev-cluster ... up --ports <port>:6443`, which
also publishes the API server on the host's loopback — and `down` stops it.

What makes a stopped cluster the same cluster when it is started again is its
state: `dev-cluster` mounts all of `/var/lib/rancher/k3s` (the datastore and
certificates, the pulled images, the local-path provisioner's volumes) from a
cache volume named after the cluster. The node name is pinned to the cluster's
name too, since the default is the container's hostname and that is new on
every start.

The container's address is also new on every start, and kube-proxy exits when
it sees a node's addresses change, which in k3s takes the whole server with it.
The entrypoint starts k3s again when that happens, as a service manager would;
the next start finds the new address already recorded. `up` waits for the node
to stay Ready across that restart before it returns.

### Kubeconfigs

`up` writes `$DEVENV_STATE/kube/<name>.yaml`, with the context named after the
cluster. Each file holds one cluster and nothing else, and a cluster is selected
by pointing `KUBECONFIG` at its file:

```bash
eval "$(scripts/dev-cluster.sh env dev1)"
```

That is deliberate. `KUBECONFIG` is the one selection every tool honours —
kubectl, helm, helmfile, k9s — and it belongs to the shell: nothing another
terminal does can change which cluster this one talks to, and a command pulled
out of history runs against the cluster the shell was pointed at or fails. A
shell pointed at a dev cluster holds no production credentials at all.
Switching contexts inside one shared kubeconfig is the opposite on both counts:
`kubectl config use-context` rewrites a file every shell reads.

The admin credential is a token generated on the first `up` and kept beside the
cluster's port in `$DEVENV_STATE/dev-clusters/<name>/`, so the kubeconfig stays
valid when the cluster is resumed. The CA comes from `/cacerts`, fetched over
the loopback tunnel.

### Testing an existing cluster

`integration-test` takes `--cluster` and `--kubeconfig` to deploy to
and test a cluster that is already running instead of creating one:

```bash
dagger call kubernetes integration-test \
  --cluster=tcp://localhost:<port> --kubeconfig=file:<kubeconfig>
```

`scripts/dev-cluster.sh test <name>` fills both in. The kubeconfig names the API
server as the host reaches it, so the workflow rewrites its copy to the
service's alias on the same port; the certificate already carries that name.
The cluster is left running, with whatever was deployed to it.

A run against an existing cluster is never cached. It is run in place rather
than through the module's own API, and its execs carry a random run id: a
verdict about a cluster that outlives the run says nothing about the next one.

### Limitations

- The state lives in a Dagger cache volume, which the engine may prune when it
  is short of disk. A dev cluster is for work in progress, not for anything
  that has to survive.
- Pods lose their `emptyDir` contents when the cluster is stopped, as they would
  across a node reboot.
- The API server is published on the loopback interface only, and the admin
  token is stored unencrypted under `$DEVENV_STATE`.
