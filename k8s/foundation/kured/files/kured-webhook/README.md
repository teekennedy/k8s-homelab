# kured-webhook

Small HTTP webhook used by kured to prepare node before a reboot and restore node functionality after a reboot.

Webhook functions:
- Creates Alertmanager silences and emits a KuredNodeRebooting alert when a node is drained/uncordoned.
- Triggers Longhorn node and disk eviction when a node is being drained for reboot.

## Running tests

```sh
cd k8s/foundation/kured/files/kured-webhook
uv sync --dev
uv run pytest
```

`uv sync` will create a local virtual environment in `.venv` for the tests.

## How It Works

When Kured drains a node:

1. **Drain Event**: Kured sends `event=drain node=<nodename>` to the webhook
2. **Silence Expected Reboot Alerts**: Kured creates timed silences for alerts like `KubeNodeUnreachable` that are expected to fire during a reboot.
3. **Reboot Alert** Kured sends an alert to Alertmanager called `KuredNodeRebooting` so users know that the node is going to be rebooted.
   Note that this alert does not prevent kured from rebooting the node, as kured checks for firing alert rules, and this alert is not associated with a rule.
4. **Longhorn Eviction**: Webhook calls Longhorn API to:
   - Set node `allowScheduling: false` and `evictionRequested: true`
   - Set all disks on the node to `allowScheduling: false` and `evictionRequested: true`
5. **Longhorn Evacuates**: Longhorn automatically starts evacuating replicas to other nodes
6. **Instance Manager Released**: Once replicas are evacuated, the instance manager PDB is removed
7. **Drain Proceeds**: Kured can now evict the instance manager pod and drain the node
8. **Node Reboots**: Kured reboots the node

When the node comes back:

1. **Uncordon Event**: Kured sends `event=uncordon node=<nodename>` to the webhook
2. **Longhorn Restore**: Webhook calls Longhorn API to:
   - Set node `allowScheduling: true` and `evictionRequested: false`
   - Set all disks to `allowScheduling: true` and `evictionRequested: false`
3. **Silence Expected Post-reboot Alerts**: Kured creates timed silences for alerts like `KubePodNotReady` that are expected to fire after a reboot.
4. **Replicas Rebuild**: Longhorn automatically rebuilds replicas on the restored node

## RBAC Permissions

The webhook service account has ClusterRole permissions to:
- Get, List, Update, and Patch Longhorn node resources

## Error Handling

The webhook will:
- Log errors if Longhorn eviction fails
- Continue with alert silencing even if Longhorn operations fail
- Return HTTP 200 to Kured so it proceeds with the drain

This ensures that even if Longhorn is not installed or unavailable, node reboots will still proceed.
