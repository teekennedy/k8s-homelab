# Homelab terraform

These modules contain everything externally needed to setup the k8s cluster. It uses OpenTofu for applying state.

## State

State lives in S3 with DynamoDB locking, provisioned by the [`tfstate-backend/`](tfstate-backend) module:

| | |
| --- | --- |
| Bucket | `missingtoken-terraform-state-us-west-2` |
| Lock table | `missingtoken-terraform-state-us-west-2` |
| Key | `<var.environment>/<module path relative to terraform/>/terraform.tfstate` |

So the root module writes to `k8s-homelab/terraform.tfstate` and `lan/` writes to
`k8s-homelab/lan/terraform.tfstate`. Both the bucket and the table take the region
as a suffix, resolved from `var.aws_region` in the backend block and from the
`aws_region` data source when the resources themselves are created.

Locks are taken in DynamoDB *and* as an S3 conditional-write lock file
(`use_lockfile`), so either mechanism alone will fail a concurrent apply.

Running any module therefore needs AWS credentials in the environment. The `s3`
backend is built into OpenTofu, so `lan/` uses it without declaring the AWS
provider -- it only needs the credentials, not the plugin.

The root module and `lan/` both encrypt state client side with OpenTofu's
`encryption` block before it is uploaded, because their state caches decrypted
secrets -- IAM access keys, Cloudflare API tokens and Kubernetes secrets for the
root module, the UniFi API key for `lan/`. `tfstate-backend/` is not encrypted:
it holds nothing but bucket and table metadata.

The passphrase comes from the `tofu_state_passphrase` key in each module's own
`tfvars.sops.yaml` and is passed in as `TF_VAR_state_passphrase`.

An `encryption` block is evaluated statically, before any provider starts, so it
cannot read `data.sops_file.tfvars` the way the rest of each module does.

To bootstrap, copy tfvars.sops.example.yaml to tfvars.sops.yaml and then run `sops edit tfvars.sops.yaml` to fill in the values.

To plan / apply the root or `lan/` module, `cd` to the module's directory and run:

```sh
TF_VAR_state_passphrase="$(bash -c 'sops decrypt tfvars.sops.yaml | yq .tofu_state_passphrase')" tofu <action>
```

`tfstate-backend/` needs no passphrase, just `tofu <action>`.

### Recovering a bad apply

The bucket is versioned and keeps superseded state for 90 days. To roll back:

```sh
aws s3api list-object-versions --bucket missingtoken-terraform-state-us-west-2 \
  --prefix k8s-homelab/terraform.tfstate --query 'Versions[].[LastModified,VersionId]' --output text
aws s3api get-object --bucket missingtoken-terraform-state-us-west-2 \
  --key k8s-homelab/terraform.tfstate --version-id <id> restored.tfstate
```

### Re-bootstrapping the backend

`tfstate-backend/` stores its own state in the bucket it creates. On a fresh AWS
account, move `tfstate-backend/backend.tf` aside, run `tofu init && tofu apply`
against local state, then restore it and run `tofu init -migrate-state`.

## SES bounce/complaint notifications

Set `notification_email` in `terraform/tfvars.sops.yaml` to the address that should receive SES bounce/complaint alerts.

After applying, AWS SNS will send a subscription confirmation email to that address. You must click the confirmation link or SES will not publish notifications.

SES account-level suppression is enabled for bounces and complaints. This automatically blocks sends to any address that hard-bounces or complains, protecting sender reputation. Remove an address from the suppression list before re-sending:

```sh
aws sesv2 list-suppressed-destinations
aws sesv2 delete-suppressed-destination --email-address user@example.com
```

## Getting restic-backup-user creds

After applying the backup module, use the following commands to get the access keys for restic-backup-user:

```sh
TF_VAR_state_passphrase="$(bash -c 'sops decrypt $DEVENV_ROOT/terraform/tfvars.sops.yaml | yq .tofu_state_passphrase')" \
  tofu -chdir="$DEVENV_ROOT/terraform" output -show-sensitive -json \
  | jq -r '.backup_access_keys.value["restic-backup-user"] | to_entries | map(.key + "=" + .value)[]' \
  | pbcopy
```

Save these values to nix/modules/restic/secrets.enc.yaml as environment variables under the `restic_env_file` yaml key.
