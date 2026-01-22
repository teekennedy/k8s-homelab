# Homelab terraform

These modules contain everything externally needed to setup the k8s cluster. It uses OpenTofu for applying state.

The state is encrypted with a password using the `state_passphrase` variable. This and other variables such as CloudFlare / AWS credentials are stored in the terraform.tfstate file, which is loaded into opentofu using the terraform-sops-provider.

To bootstrap, copy tfvars.sops.example.yaml to tfvars.sops.yaml and then run `sops edit tfvars.sops.yaml` to fill in the values.

To plan / apply, run `TF_VAR_state_passphrase="$(bash -c 'sops decrypt tfvars.sops.yaml | yq .tofu_local_state_passphrase')" tofu <action>`. This passes the tofu state passphrase from the encrypted tfvars.sops.yaml into opentofu so it can read / write the encrypted state.

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
TF_VAR_state_passphrase="$(bash -c 'sops decrypt $DEVENV_ROOT/terraform/tfvars.sops.yaml | yq .tofu_local_state_passphrase')" \
  tofu -chdir="$DEVENV_ROOT/terraform" output -show-sensitive -json \
  | jq -r '.backup_access_keys.value["restic-backup-user"] | to_entries | map(.key + "=" + .value)[]' \
  | pbcopy
```

Save these values to nix/modules/restic/secrets.enc.yaml as environment variables under the `restic_env_file` yaml key.
