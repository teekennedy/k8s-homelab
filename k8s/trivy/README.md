# Trivy audit

`dagger check helmfile` audits every release's rendered manifests with
[Trivy](https://trivy.dev) (`helmfile/releases/audit`). Three things run, and a
finding of any severity fails the release:

- Trivy's built-in Kubernetes checks (`KSV-*`): the Pod Security Standards,
  RBAC grants that lead to the whole cluster, deprecated APIs.
- The custom checks in `checks/` (`HL-*`), for what Trivy has no check for or
  checks more strictly than this cluster wants.
- Trivy's secret scanner, which looks for credentials (API keys, tokens,
  private keys) anywhere in a manifest.

Schema validation is not Trivy's job: `helmfile/releases/validate` does that
with kubeconform.

## Custom checks

| ID | Fails |
| --- | --- |
| `HL-0001` | A long-running container with no liveness probe |
| `HL-0002` | A long-running container with no readiness probe |
| `HL-0003` | `imagePullPolicy: Always` |
| `HL-0004` | An image not pinned by digest (pending) |
| `HL-0005` | A Longhorn volume outside the `backup` and `ephemeral` recurring-job groups |
| `HL-0006` | A password, token or key environment variable set to a literal |
| `HL-0007` | A PodDisruptionBudget that allows no disruptions |
| `HL-0008` | An Ingress without TLS |
| `HL-0009` | A container that can run as root |
| `HL-0010` | A container with no CPU request, memory request or memory limit, in any workload including Jobs and CronJobs |

Jobs, CronJobs and init containers that run to completion are not
"long-running"; init containers declared as sidecars are.

## Switching a check off

`trivyignore.yaml` lists the checks that run for no release, each with the
reason: checks this cluster does not want, checks a custom one replaces, and
checks that are wanted but still fail (`Pending`). The built-in checks ship
with the trivy binary, so everything not listed there runs, including a check
a trivy upgrade adds.

## Exempting a resource

Trivy audits a release as one file per resource, which is the path a failure
prints: `<Kind>/<namespace>/<name>.yaml`, or `<Kind>/<name>.yaml` when the
rendered manifest sets no namespace. Resources in a `kind: List` are audited
one by one. A chart exempts a resource from a check by listing
that path in a `trivyignore.yaml` beside its `Chart.yaml`, in
[Trivy's format](https://trivy.dev/docs/latest/guide/configuration/filtering/#trivyignoreyaml):

```yaml
misconfigurations:
  # kured reboots a node from a privileged container.
  - id: KSV-0017 # privileged
    paths: &kured [DaemonSet/kured/kured.yaml]
  - id: KSV-0001 # privilege escalation
    paths: *kured
secrets:
  - id: private-key
    paths: [ConfigMap/default/test-fixtures.yaml]
```

An entry with no `paths` exempts the whole release. `paths` are globs, matched
against the whole path. Give the reason in a comment, and prefer fixing the
manifest when the chart allows it.

## Adding a custom check

1. Write `checks/<name>.rego` in package `user.kubernetes.<ID>`, with the next
   `HL-` ID in its metadata. `checks/lib/workload.rego` finds a workload's pod
   and containers.
2. Add manifests it must fail and must pass as `tests/<ID>/fail*.yaml` and
   `tests/<ID>/pass*.yaml`. `dagger check helmfile/test` runs the checks over
   them, and fails for a check with no fixtures.
3. `dagger check helmfile` to see what the releases make of it.
