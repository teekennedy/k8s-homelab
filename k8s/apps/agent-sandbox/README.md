# agent-sandbox

The [kubernetes-sigs/agent-sandbox][upstream] controller. It provides one CRD —
`Sandbox` (`agents.x-k8s.io/v1beta1`) — which materialises a single stateful pod,
optionally with its own Service and PVCs, and reports its lifecycle as status
conditions.

Nothing in this cluster uses it directly. It exists so that
[`k8s/apps/archon`](../archon) can create a fresh, single-use pod per agent
attempt and be told, through one condition, whether that attempt succeeded.

[upstream]: https://github.com/kubernetes-sigs/agent-sandbox

## Layout

Upstream publishes releases only as flat manifests, not a Helm chart, so this
is a Kustomize app: `kustomization.yaml` pulls the pinned release manifest
straight from GitHub and patches in this cluster's security posture and
observability (`patches/deployment.yaml`, `networkpolicy.yaml`,
`servicemonitor.yaml`). Renovate bumps the pinned tag in the release URL via
the `kustomize` customManager in `renovate.json` — the manifest, controller
image, and CRD all move together since they come from the same URL.

The `SandboxTemplate` / `SandboxClaim` / `SandboxWarmPool` extensions and the
sandbox router are left out of the pulled manifest: Archon creates and deletes
one Sandbox per attempt directly, and reads its progress from the pod log
through the API server rather than dialing it.

## Upgrading

Bump the tag in `kustomization.yaml`'s `resources` URL (Renovate does this).
Diff the new release's `ClusterRole` and `Deployment` args against
`patches/deployment.yaml` while you're at it, in case upstream added a flag or
RBAC rule this patch needs to account for.

## Security notes

The controller holds `create`/`delete` on pods cluster-wide — that is inherent
to what it does. What it does *not* hold is `secrets`, `pods/exec`, or any write
to RBAC or CRDs.

`networkpolicy.yaml` restricts it to DNS plus the kube API outbound, and
scraping from `monitoring-system` inbound. It has no reason to reach anything
else, including the sandboxes it creates.
