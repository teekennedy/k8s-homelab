# monitoring-system

## Grafana dashboards

Dashboards reach Grafana by two routes:

| Source | Mechanism | Editable in the UI |
|---|---|---|
| `dashboards/*.json` | [Git Sync](https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/) | Yes, saved as a branch for review |
| `mixins/` (generated from jsonnet) and `grafana_dashboard: "1"` ConfigMaps in other charts | dashboard sidecar | No |

### Git Sync

Grafana syncs `dashboards/` straight from `main` on Forgejo, every 60s. Argo CD
isn't involved, so a merged dashboard change goes live without a sync.
Everything lands in the `k8s-homelab` folder in Grafana.

To edit a dashboard:

1. Change it in the UI and **Save**. Grafana pushes the change to a new branch
   as the `grafana` Forgejo user. Direct commits to `main` are disabled.
2. Open a PR for that branch in Forgejo. Pure-git repositories have no provider
   API, so Grafana can't open the PR itself.
3. Merge. Grafana pulls the change on its next sync.

Hand-written dashboards go in `dashboards/` and need a stable `uid`. Git Sync
can't take over a UID that another source, such as a sidecar ConfigMap, already
uses in Grafana.

Wiring:

- The `grafana` Forgejo user and its token (Secret `grafana-forgejo-user`) are
  declared in `k8s/platform/forgejo/values.yaml`. Its `Dashboards` team gives it
  write access to this repo only.
- `grafana.ini` `[provisioning]` enables the pure-git repository type. It also
  allowlists `git.msng.to`, which resolves to a LAN address.
- The Repository object lives in Grafana's own API, not in the cluster. The
  `grafana-git-sync` PostSync Job (`templates/grafana-git-sync.yaml`) applies it
  from `gitSync` in `values.yaml` on every Argo CD sync.

Repository status and sync errors: **Administration → Provisioning** in Grafana.
