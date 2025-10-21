# Homelab terraform

These modules contain everything externally needed to setup the k8s cluster. It uses OpenTofu for applying state.

The state is encrypted with a password using the `state_passphrase` variable. This and other variables such as CloudFlare / AWS credentials are stored in the terraform.tfstate file, which is loaded into opentofu using the terraform-sops-provider.

To bootstrap, copy tfvars.sops.example.yaml to tfvars.sops.yaml and then run `sops edit tfvars.sops.yaml` to fill in the values.

To plan / apply, run `TF_VAR_state_passphrase="$(bash -c 'sops decrypt tfvars.sops.yaml | yq .tofu_local_state_passphrase')" tofu <action>`. This passes the tofu state passphrase from the encrypted tfvars.sops.yaml into opentofu so it can read / write the encrypted state.

## Getting restic-backup-user creds

After applying the backup module, use the following commands to get the access keys for restic-backup-user:

```sh
# access_key_id
TF_VAR_state_passphrase="$(bash -c 'sops decrypt $DEVENV_ROOT/terraform/tfvars.sops.yaml | yq .tofu_local_state_passphrase')" \
  tofu -chdir="$DEVENV_ROOT/terraform" output -show-sensitive -json \
  | jq -r '.backup_access_keys.value["restic-backup-user"] | "AWS_ACCESS_KEY_ID=" + .access_key_id'

# secret_access_key
echo -n "AWS_SECRET_ACCESS_KEY="
TF_VAR_state_passphrase="$(bash -c 'sops decrypt $DEVENV_ROOT/terraform/tfvars.sops.yaml | yq .tofu_local_state_passphrase')" \
  tofu -chdir="$DEVENV_ROOT/terraform" output -show-sensitive -json \
  | jq -r '.backup_access_keys.value["restic-backup-user"] | .encrypted_secret_access_key' \
  | base64 -d \
  | gpg --decrypt 2>/dev/null
```

Save these values to modules/restic/secrets.enc.yaml as environment variables under the `restic_env_file` yaml key.
